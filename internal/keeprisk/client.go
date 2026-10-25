package keeprisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/audit"
	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
	"github.com/xrzlizheng/keeprisk-mcp/internal/params"
)

// maxResponseBytes 返回网关响应体大小限制。
// 环境变量 KEEPRISK_MAX_RESPONSE_MB 控制（默认 4MB）。
// 进程内只解析一次（sync.OnceValue），避免每次请求重复读环境变量。
var maxResponseBytes = sync.OnceValue(func() int64 {
	mb := 4
	if v := strings.TrimSpace(os.Getenv("KEEPRISK_MAX_RESPONSE_MB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			mb = n
		}
	}
	return int64(mb) << 20
})

const defaultBase = "http://host:port"

// routePcodeRe 路由配置键的 pcode 形态**粗筛**（与 catalog.pcodeRe 同口径，宽松）。
// 它接不住少一位的笔误（E_007_144 形态合法）——那类错误由下方对目录的存在性
// 校验兜底拦截；形态检查只负责快速拒绝明显非 pcode 的键（如 E0071441、中文名）。
var routePcodeRe = regexp.MustCompile(`^[EP]_\d+_\d+$`)

// statusHints 网关状态码 → 可执行指引。
// 文案要求：给出"下一步动作"，而不是复述上游文案（2026-09-16 盲测：模型面对
// 无指引的 500/101 只能盲目重试或放弃）。
var statusHints = map[int]string{
	100: "查询成功",
	101: "参数不符合接口要求；可先用 validate_keeprisk_params 本地核对参数名与必填项，或用 get_keeprisk_api 查看入参 schema",
	103: "账户不存在，请确认 AppCode/账户状态",
	104: "IP 受限，请联系平台加白",
	105: "账号已过期，请联系平台续期",
	107: "该账户未开通此接口，请联系平台开通；本次调用未计费",
	108: "通道关闭，请联系平台",
	109: "账户资金不足，请联系平台充值",
	110: "查询成功，无记录",
	500: "上游未报告具体原因（可能是参数不受支持或服务异常）；建议核对参数后重试，持续失败请反馈接口名",
}

type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

type APIError struct {
	Msg     string
	Code    any
	Payload any
}

func (e *APIError) Error() string { return e.Msg }

type Client struct {
	base       string
	http       *http.Client
	maxInflight int
	sem        chan struct{}
	// routes 按 pcode 覆盖网关地址与出站 AppCode（部分接口部署在独立环境/专用账户，
	// 如政策 V2 E_007_1441/E_007_1451 在独立测试网关）；命中路由时出站凭证用路由
	// 配置的 app_code，入站鉴权（RequireAppCode）仍按调用方执行。
	routes map[string]pcodeRoute
}

// pcodeRoute 单个 pcode 的网关路由覆盖。
type pcodeRoute struct {
	baseURL string
	appCode string
}

var (
	clientOnce sync.Once
	defaultCli *Client
)

// 重试参数（与 Python 版 client.py 对齐）
const (
	maxRetries       = 3
	retryBackoffBase = 500 * time.Millisecond
)

// isRetryableHTTPStatus 判断 HTTP 状态码是否可重试（5xx 服务端错误）。
// 4xx 客户端错误不重试（与 Python 版 _is_retryable 一致）。
func isRetryableHTTPStatus(code int) bool {
	return code >= 500 && code < 600
}

// isRetryableNetErr 判断网络错误是否可重试。
// 上下文取消/截止时间不重试（调用方主动取消）。
func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func DefaultClient() (*Client, error) {
	var err error
	clientOnce.Do(func() {
		base := stringsTrim(os.Getenv("KEEPRISK_API_BASE"), defaultBase)
		if strings.Contains(base, "host:port") {
			// 启动期错误仅落服务端日志，不进对客输出：不给示例地址、不回显配置值
			err = &ConfigError{Msg: "未配置 KEEPRISK_API_BASE（服务端网关地址）。请检查服务端部署配置。"}
			return
		}
		// 启动期校验：必须 http(s)://，含 host，避免因笔误导致运行时连接错误
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			err = &ConfigError{Msg: "KEEPRISK_API_BASE 必须以 http:// 或 https:// 开头。"}
			return
		}
		if strings.HasPrefix(base, "http://") && strings.TrimSpace(os.Getenv("KEEPRISK_REQUIRE_TLS")) == "true" {
			err = &ConfigError{Msg: "KEEPRISK_API_BASE 为 http://，但 KEEPRISK_REQUIRE_TLS=true。请使用 https://。"}
			return
		}
		max := 200
		if v := os.Getenv("KEEPRISK_MAX_INFLIGHT"); v != "" {
			if n, e := strconv.Atoi(v); e == nil && n > 0 {
				max = n
			}
		}
		timeout := 60 * time.Second
		if v := os.Getenv("KEEPRISK_HTTP_TIMEOUT"); v != "" {
			if d, e := time.ParseDuration(v); e == nil {
				timeout = d
			}
		}
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		defaultCli = &Client{
			base: strings.TrimRight(base, "/"),
			http: &http.Client{Timeout: timeout, Transport: transport},
			maxInflight: max,
			sem:        make(chan struct{}, max),
		}
		routes, rerr := parseAPIRoutes(os.Getenv("KEEPRISK_API_ROUTES"), strings.TrimSpace(os.Getenv("KEEPRISK_REQUIRE_TLS")) == "true")
		if rerr != nil {
			err = rerr
			return
		}
		defaultCli.routes = routes
	})
	if err != nil {
		return nil, err
	}
	return defaultCli, nil
}

// parseAPIRoutes 解析 KEEPRISK_API_ROUTES（JSON）：
//
//	{"E_007_1441":{"base_url":"https://apitest.example.com:10001","app_code":"..."},
//	 "E_007_1451":{...}}
//
// 校验口径与主网关一致（http(s) 前缀、REQUIRE_TLS 一致性、AppCode 字符集），
// 并对 pcode 键做「形态 + 存在性」双重校验：路由键是**手写**配置，少一位的笔误
// （E_007_144）或形态合法但不存在/已弃用的键都会静默不命中、回落主网关——
// 启动期 fail-fast 远比运行期排查便宜。
func parseAPIRoutes(raw string, requireTLS bool) (map[string]pcodeRoute, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var m map[string]struct {
		BaseURL string `json:"base_url"`
		AppCode string `json:"app_code"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 不是合法 JSON（期望 {pcode:{base_url,app_code}}）。"}
	}
	out := make(map[string]pcodeRoute, len(m))
	for pcode, r := range m {
		p := strings.ToUpper(strings.TrimSpace(pcode))
		if p == "" {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 存在空 pcode 键。"}
		}
		if !routePcodeRe.MatchString(p) {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 中 " + p + " 不是合法 pcode（期望形如 E_007_1441）。"}
		}
		// 存在性校验：键必须是**当前有效**接口。历史编码（已下线）不再自动升级、
		// 调用侧本地拒绝，GetByPcode 对历史/未知编码返回 nil——此类路由永不命中，
		// 同样按配置错误拦截。
		if entry := catalogEntry(p); entry == nil || entry.Pcode != p {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 中 " + p + " 不是当前有效接口（不存在或已弃用，请配在最新版 pcode 上）。"}
		}
		base := strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 中 " + p + " 的 base_url 必须以 http:// 或 https:// 开头。"}
		}
		if strings.HasPrefix(base, "http://") && requireTLS {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 中 " + p + " 的 base_url 为 http://，但 KEEPRISK_REQUIRE_TLS=true。"}
		}
		if !auth.ValidAppCode(r.AppCode) {
			return nil, &ConfigError{Msg: "KEEPRISK_API_ROUTES 中 " + p + " 的 app_code 格式非法（仅允许 1-64 位字母/数字/下划线/连字符）。"}
		}
		out[p] = pcodeRoute{baseURL: base, appCode: r.AppCode}
	}
	return out, nil
}

// catalogEntry 查目录条目（间接层便于测试注入；生产走内嵌目录）。
var catalogEntry = func(pcode string) *catalog.APIEntry {
	c, err := catalog.Default()
	if err != nil {
		return nil
	}
	return c.GetByPcode(pcode)
}

// routeFor 返回 pcode 的路由覆盖（无路由时 ok=false）。pcode 归一大写。
func (c *Client) routeFor(pcode string) (pcodeRoute, bool) {
	rt, ok := c.routes[strings.ToUpper(strings.TrimSpace(pcode))]
	return rt, ok
}

func (c *Client) CallDataAPI(ctx context.Context, pcode string, body map[string]any) (map[string]any, error) {
	appCode, err := auth.RequireAppCode(ctx)
	if err != nil {
		return nil, &ConfigError{Msg: err.Error()}
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	strBody := params.SerializeBody(body)
	url := c.base + "/api/data"
	// pcode 路由覆盖：命中 KEEPRISK_API_ROUTES 时改走指定网关与出站 AppCode
	// （入站鉴权已在上方 RequireAppCode 完成，路由仅覆盖出站凭证）。
	outAppCode := appCode
	routed := false
	if rt, ok := c.routeFor(pcode); ok {
		url = rt.baseURL + "/api/data"
		outAppCode = rt.appCode
		routed = true
	}
	payload, err := json.Marshal(strBody)
	if err != nil {
		return nil, err
	}

	// 前置检查已通过：此后必然真实发出网关请求，计入审计与计费还原。
	// 缓存命中/本地校验失败/配额拒绝均在 QueryImpl 提前返回，不经过本函数，故天然不计。
	start := time.Now()
	audit.IncGatewayCount(ctx)
	data, attempts, callErr := c.callWithRetry(ctx, url, pcode, outAppCode, payload)
	auditGatewayCall(ctx, pcode, time.Since(start), attempts, data, callErr, routed)
	return data, callErr
}

// callWithRetry 执行网关调用并返回响应数据与实际发出的 HTTP 尝试次数。
//
// 与 Python 版 client.py 对齐：maxRetries=3、retryBackoffBase=0.5s、指数退避 2^n；
// 网络错误或 5xx 重试，4xx/业务错误不重试。
func (c *Client) callWithRetry(ctx context.Context, url, pcode, appCode string, payload []byte) (map[string]any, int, error) {
	var resp *http.Response
	var raw []byte
	var readErr error
	var truncated bool
	limit := maxResponseBytes()
	var lastErr error
	var doErr error
	attempts := 0
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			wait := retryBackoffBase * (1 << (attempt - 1))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, attempts, ctx.Err()
			}
		}
		attempts++
		// 每次重试重建 request（body reader 需可重置）
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if reqErr != nil {
			return nil, attempts, reqErr
		}
		req.Header.Set("AppCode", appCode)
		req.Header.Set("pcode", pcode)
		req.Header.Set("Content-Type", "application/json;charset=utf-8")

		resp, doErr = c.http.Do(req)
		if doErr != nil {
			lastErr = doErr
			if isRetryableNetErr(doErr) && attempt < maxRetries-1 {
				continue
			}
			// 原始错误（含上游 URL/host）只落服务端日志；对客文案保持中性
			log.Printf("keeprisk: 网关请求失败（重试 %d 次后）: %v", maxRetries, doErr)
			return nil, attempts, &APIError{Msg: "上游网关暂时不可用，请稍后重试。"}
		}
		// 多读 1 字节用于区分"网关返回非 JSON"与"响应超限被截断"
		// 注意：用 = 赋值而非 := 声明，避免在 for 循环体内遮蔽函数级 raw 变量
		raw, readErr = io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if isRetryableNetErr(readErr) && attempt < maxRetries-1 {
				continue
			}
			log.Printf("keeprisk: 读取网关响应失败（重试 %d 次后）: %v", maxRetries, readErr)
			return nil, attempts, &APIError{Msg: "读取上游响应失败，请稍后重试。"}
		}
		truncated = int64(len(raw)) > limit
		if truncated {
			raw = raw[:limit]
		}
		// 5xx 可重试；4xx 不重试直接返回
		if isRetryableHTTPStatus(resp.StatusCode) && attempt < maxRetries-1 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, attempts, &APIError{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 500)), Payload: string(raw)}
		}
		// 成功：跳出重试循环
		lastErr = nil
		break
	}
	if lastErr != nil {
		return nil, attempts, &APIError{Msg: fmt.Sprintf("请求失败（重试 %d 次后）: %v", maxRetries, lastErr)}
	}
	if truncated {
		// 对客文案不带环境变量名（服务端配置细节不外泄）
		return nil, attempts, &APIError{Msg: fmt.Sprintf("响应超过大小限制（%d 字节），已被截断。", limit)}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, attempts, &APIError{Msg: fmt.Sprintf("响应非 JSON: %s", truncate(string(raw), 300))}
	}
	code := data["code"]
	if !isSuccessCode(code) {
		hint := ""
		if ci := codeInt(code); ci != 0 {
			if h, ok := statusHints[ci]; ok {
				hint = "（" + h + "）"
			}
		}
		msg, _ := data["message"].(string)
		if msg == "" {
			msg, _ = data["msg"].(string)
		}
		if msg == "" {
			msg = "请求失败"
		}
		return nil, attempts, &APIError{Msg: fmt.Sprintf("code=%v %s%s", code, msg, hint), Code: code, Payload: data}
	}
	return data, attempts, nil
}

// auditGatewayCall 落一条网关层审计记录（逐 pcode 的真实调用）。
//
// 开关：KEEPRISK_AUDIT_PER_PCODE=false 关闭逐接口明细（默认开）。
// 无论成功失败都记录——失败调用同样占用网关资源，是"成功才计费"口径的核对依据。
func auditGatewayCall(ctx context.Context, pcode string, latency time.Duration, attempts int, data map[string]any, callErr error, routed bool) {
	if !audit.PerPcodeEnabled() {
		return
	}
	r := audit.Default().FromContext(ctx)
	r.Method = audit.MethodGateway
	r.Tool = audit.ToolFromContext(ctx)
	r.Pcode = pcode
	r.LatencyMS = latency.Milliseconds()
	if callErr != nil {
		r.Status = "error"
		r.ErrorMsg = callErr.Error()
		if e, ok := callErr.(*APIError); ok {
			if e.Code != nil {
				r.ErrorCode = fmt.Sprint(e.Code)
			}
		}
	} else {
		r.Status = "ok"
	}
	// attempts > 1 表示经历重试：网关实际被请求多次，是成本高于计数的信号
	r.SetExtra("attempts", attempts)
	// pcode 走了 KEEPRISK_API_ROUTES 覆盖网关（计费挂在路由配置的出站账户上，
	// 与记录中 app_code 字段的调用方账户不同，对账时以本标记区分）
	if routed {
		r.SetExtra("routed", true)
	}
	// 网关侧计费字段（当前上游未返回，返回即自动落库）：
	// 有了它即可与扇出还原值对账，无需再按倍数估算。
	if v, ok := data["charge"]; ok {
		r.SetExtra("charge", v)
	}
	audit.Default().Record(r)
}

func isSuccessCode(code any) bool {
	switch v := code.(type) {
	case float64:
		return int(v) == 100 || int(v) == 110
	case int:
		return v == 100 || v == 110
	case string:
		return v == "100" || v == "110"
	default:
		return false
	}
}

func codeInt(code any) int {
	switch v := code.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

func stringsTrim(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}