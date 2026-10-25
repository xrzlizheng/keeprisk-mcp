package keeprisk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/audit"
	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
)

// auditLogPath 是本包测试共用的审计落盘文件（TestMain 初始化）。
//
// 走真实路径（env → audit.Default → 文件）而非注入 mock，保证埋点开关与
// 落盘行为一并被覆盖。
var auditLogPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "krmcp-audit")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: temp dir:", err)
		os.Exit(1)
	}
	auditLogPath = filepath.Join(dir, "audit.jsonl")
	os.Setenv("KEEPRISK_AUDIT_PATH", auditLogPath)
	os.Setenv("KEEPRISK_AUDIT_ASYNC", "false")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newTestClient 构造指向测试网关的客户端（绕过 DefaultClient 的 sync.Once 与 env 校验）。
func newTestClient(url string) *Client {
	return &Client{
		base:        url,
		http:        &http.Client{Timeout: 10 * time.Second},
		maxInflight: 8,
		sem:         make(chan struct{}, 8),
	}
}

// gatewayAuditRecord 网关层审计记录（只取本测试关心的字段）
type gatewayAuditRecord struct {
	Method  string `json:"method"`
	Tool    string `json:"tool"`
	Pcode   string `json:"pcode"`
	Status  string `json:"status"`
	Code    string `json:"error_code"`
	Attempt int    `json:"-"`
	Extra   map[string]any
}

func readGatewayRecords(t *testing.T) []gatewayAuditRecord {
	t.Helper()
	b, err := os.ReadFile(auditLogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取审计文件: %v", err)
	}
	var out []gatewayAuditRecord
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			Method string         `json:"method"`
			Tool   string         `json:"tool"`
			Pcode  string         `json:"pcode"`
			Status string         `json:"status"`
			Code   string         `json:"error_code"`
			Extra  map[string]any `json:"extra"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("解析审计行失败: %v; line=%s", err, line)
		}
		if raw.Method != audit.MethodGateway {
			continue
		}
		rec := gatewayAuditRecord{
			Method: raw.Method, Tool: raw.Tool, Pcode: raw.Pcode,
			Status: raw.Status, Code: raw.Code, Extra: raw.Extra,
		}
		if raw.Extra != nil {
			if v, ok := raw.Extra["attempts"].(float64); ok {
				rec.Attempt = int(v)
			}
		}
		out = append(out, rec)
	}
	return out
}

// testCtx 构造带 AppCode + 工具名 + 网关计数器的请求 ctx（对齐工具层注入形态）。
func testCtx(tool string) context.Context {
	ctx := audit.WithTool(audit.WithGatewayCounter(context.Background()), tool)
	return auth.WithAppCode(ctx, "TestAppCode")
}

// ─────────── 核心：聚合工具的扇出按 pcode 逐条落盘 ───────────

func TestGatewayAuditRecordsEachPcode(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("pcode"))
		mu.Unlock()
		fmt.Fprint(w, `{"code":100,"data":{"ok":true}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := testCtx("enterprise_extended_profile")
	want := []string{"E_007_0501", "E_007_0901", "E_007_0601", "E_007_1021"}

	for _, pc := range want {
		if _, err := c.CallDataAPI(ctx, pc, map[string]any{"entName": "测试科技有限公司"}); err != nil {
			t.Fatalf("CallDataAPI(%s): %v", pc, err)
		}
	}

	// 1) 请求级计数器给出该工具实际网关调用总数（计费单位还原依据）
	n, ok := audit.GatewayCountFromContext(ctx)
	if !ok {
		t.Fatal("ctx 未注入网关计数器，gateway_calls 将无法统计")
	}
	if n != int64(len(want)) {
		t.Fatalf("gateway_calls = %d, want %d", n, len(want))
	}

	// 2) 逐接口明细：每个 pcode 一条记录，可归因到工具
	recs := readGatewayRecords(t)
	if len(recs) != len(want) {
		t.Fatalf("网关审计记录数 = %d, want %d; recs=%+v", len(recs), len(want), recs)
	}
	gotPcodes := make(map[string]int)
	for _, r := range recs {
		if r.Method != audit.MethodGateway {
			t.Fatalf("method = %q, want %q", r.Method, audit.MethodGateway)
		}
		if r.Tool != "enterprise_extended_profile" {
			t.Fatalf("tool = %q, want enterprise_extended_profile（须可归因）", r.Tool)
		}
		if r.Status != "ok" {
			t.Fatalf("status = %q, want ok", r.Status)
		}
		if r.Attempt != 1 {
			t.Fatalf("attempts = %d, want 1", r.Attempt)
		}
		gotPcodes[r.Pcode]++
	}
	for _, pc := range want {
		if gotPcodes[pc] != 1 {
			t.Fatalf("pcode %s 记录数 = %d, want 1; 全部=%v", pc, gotPcodes[pc], gotPcodes)
		}
	}

	// 3) 网关实际收到的 pcode 与审计记录一致（双向核对）
	if len(seen) != len(want) {
		t.Fatalf("网关收到 %d 次调用, want %d; seen=%v", len(seen), len(want), seen)
	}
}

// ─────────── 失败调用同样落盘（"成功才计费"的核对依据）───────────

func TestGatewayAuditRecordsGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":500,"message":"未知请求错误"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := testCtx("search_chain_enterprises")
	before := len(readGatewayRecords(t))

	if _, err := c.CallDataAPI(ctx, "E_007_1131", map[string]any{"chain": "人工智能产业链"}); err == nil {
		t.Fatal("网关 code=500 应返回错误")
	}

	recs := readGatewayRecords(t)[before:]
	if len(recs) != 1 {
		t.Fatalf("失败调用应落 1 条记录, got %d", len(recs))
	}
	if recs[0].Status != "error" {
		t.Fatalf("status = %q, want error", recs[0].Status)
	}
	if recs[0].Code != "500" {
		t.Fatalf("error_code = %q, want 500", recs[0].Code)
	}
	// 失败也占用网关资源，计入调用总数（是否计费由计费口径决定，审计不预判）
	if n, _ := audit.GatewayCountFromContext(ctx); n != 1 {
		t.Fatalf("gateway_calls = %d, want 1（失败调用同样计入）", n)
	}
}

// ─────────── 重试：逻辑调用 1 次，实际 HTTP 请求多次 ───────────

func TestGatewayAuditCountsRetryAttempts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"code":100,"data":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := testCtx("keeprisk_query")
	before := len(readGatewayRecords(t))

	if _, err := c.CallDataAPI(ctx, "E_007_0501", map[string]any{"entName": "测试"}); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}

	recs := readGatewayRecords(t)[before:]
	if len(recs) != 1 {
		t.Fatalf("重试应只落 1 条逻辑调用记录, got %d", len(recs))
	}
	if recs[0].Attempt != 2 {
		t.Fatalf("attempts = %d, want 2（重试 1 次）", recs[0].Attempt)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("网关实际收到 %d 次请求, want 2", got)
	}
	// 逻辑调用计 1 次（与扇出倍数口径一致）；物理请求次数由 attempts 体现
	if n, _ := audit.GatewayCountFromContext(ctx); n != 1 {
		t.Fatalf("gateway_calls = %d, want 1（逻辑调用）", n)
	}
}

// ─────────── 开关：关闭逐接口明细，但成本总量不丢失 ───────────

func TestGatewayAuditPerPcodeSwitchOff(t *testing.T) {
	t.Setenv("KEEPRISK_AUDIT_PER_PCODE", "false")
	if audit.PerPcodeEnabled() {
		t.Fatal("KEEPRISK_AUDIT_PER_PCODE=false 时 PerPcodeEnabled() 应为 false")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":100,"data":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := testCtx("keeprisk_query")
	before := len(readGatewayRecords(t))

	if _, err := c.CallDataAPI(ctx, "E_007_0501", map[string]any{"entName": "测试"}); err != nil {
		t.Fatalf("CallDataAPI: %v", err)
	}

	if got := len(readGatewayRecords(t)) - before; got != 0 {
		t.Fatalf("开关关闭时不应落逐接口记录, got %d", got)
	}
	// 计数器仍自增：关闭明细不影响 gateway_calls 汇总
	if n, ok := audit.GatewayCountFromContext(ctx); !ok || n != 1 {
		t.Fatalf("gateway_calls = %d(ok=%v), want 1（关闭明细仍须计数）", n, ok)
	}
}

// ─────────── 网关侧计费字段：返回即落库 ───────────

func TestGatewayAuditCapturesChargeWhenPresent(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":100,"charge":1.5,"data":{}}`)
	}))
	defer gw.Close()

	c := newTestClient(gw.URL)
	ctx := testCtx("keeprisk_query")
	before := len(readGatewayRecords(t))

	if _, err := c.CallDataAPI(ctx, "E_007_0501", map[string]any{"entName": "测试"}); err != nil {
		t.Fatalf("CallDataAPI: %v", err)
	}
	recs := readGatewayRecords(t)[before:]
	if len(recs) != 1 {
		t.Fatalf("记录数 = %d, want 1", len(recs))
	}
	if v, ok := recs[0].Extra["charge"].(float64); !ok || v != 1.5 {
		t.Fatalf("extra.charge = %v, want 1.5（网关返回即应落库）", recs[0].Extra["charge"])
	}
}

func TestGatewayAuditOmitsChargeWhenAbsent(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":100,"data":{}}`)
	}))
	defer gw.Close()

	c := newTestClient(gw.URL)
	ctx := testCtx("keeprisk_query")
	before := len(readGatewayRecords(t))

	if _, err := c.CallDataAPI(ctx, "E_007_0501", map[string]any{"entName": "测试"}); err != nil {
		t.Fatalf("CallDataAPI: %v", err)
	}
	recs := readGatewayRecords(t)[before:]
	if _, exists := recs[0].Extra["charge"]; exists {
		t.Fatal("网关未返回 charge 时不应写入该字段（避免虚构计费值）")
	}
}

// ─────────── 未注入计数器时不得伪报 0 成本 ───────────

func TestGatewayCountAbsentWhenNotInjected(t *testing.T) {
	if _, ok := audit.GatewayCountFromContext(context.Background()); ok {
		t.Fatal("未注入计数器时应返回 ok=false，避免把\"未埋点\"读成\"零成本\"")
	}
	if audit.ToolFromContext(context.Background()) != "" {
		t.Fatal("未注入工具名时应返回空串")
	}
}
