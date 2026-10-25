package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xrzlizheng/keeprisk-mcp/internal/audit"
	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
	"github.com/xrzlizheng/keeprisk-mcp/internal/cache"
	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
	"github.com/xrzlizheng/keeprisk-mcp/internal/keeprisk"
	"github.com/xrzlizheng/keeprisk-mcp/internal/quota"
)

// 本文件端到端验证「网关层逐 pcode 审计」在真实 MCP 协议链路上的行为：
//
//	client.CallTool → SDK → registerTools 回调（注入工具名+计数器）→ Services → QueryImpl
//	→ keeprisk.Client.CallDataAPI（落 gateway 记录 + 计数器自增）→ tools/call 审计（写 gateway_calls）
//
// 覆盖两个定价关键口径：
//  1. 聚合工具的扇出必须逐 pcode 可见（旧实现只落顶层 pcode_used，聚合调用 pcode 为空）；
//  2. 缓存命中不产生网关调用，gateway_calls=0（不计费）。
//
// 基础设施约束：keeprisk.DefaultClient 与 audit.Default 均为进程级单例（sync.Once），
// 故网关地址与审计文件在 TestMain 中一次性确定，测试间共享。

// ─────────────────────────── 共享测试基础设施 ───────────────────────────

var (
	sharedAuditPath string
	sharedGW        *httptest.Server
	gwHits          atomic.Int64
	// gwPcodes 记录伪造网关收到的 pcode 头（用于断言名称→编码解析结果）
	gwPcodes struct {
		mu   sync.Mutex
		last []string
	}
)

// gwPcodesSnapshot 返回当前已收到的 pcode 列表快照
func gwPcodesSnapshot() []string {
	gwPcodes.mu.Lock()
	defer gwPcodes.mu.Unlock()
	return append([]string(nil), gwPcodes.last...)
}

// gwResponse 是伪造网关的默认成功响应体（无 error 字段 → 可被缓存）。
const gwResponse = `{"code":100,"data":{"entName":"示例科技有限公司","list":[]}}`

// stubGatewayResponse 按 pcode 返回贴合接口语义的响应。
//
// 校企域需要"简称→解析→筛选"链路可端到端断言：院校信息固定解析为「清华大学」，
// 校企筛选把请求中的 text 回显为校友毕业院校（使学历二次校验可命中）。
func stubGatewayResponse(pcode string, r *http.Request) string {
	switch pcode {
	case pcodeBasicProfile:
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["entName"] == "无学历企业" {
			return `{"code":100,"data":{"entName":"无学历企业","id":"noedu-id","legalName":"无学历法人"}}`
		}
		return `{"code":100,"data":{"entName":"安徽智侒信信息技术有限公司","id":"687f2c11206abbacaa0b467a18e40666","legalName":"陈康康"}}`
	case pcodeSchoolInfo:
		name := ""
		if body, err := io.ReadAll(r.Body); err == nil {
			var m map[string]any
			if json.Unmarshal(body, &m) == nil {
				name, _ = m["universityName"].(string)
			}
		}
		if name == "无法解析" {
			return `{"code":100,"data":{"list":[]}}`
		}
		return `{"code":100,"data":{"list":[{"name":"清华大学","abbreviation":["清华"],"province":"北京市"}]}}`
	case pcodeSchoolFilter:
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		// companyId 维度：返回该企业人员的学历明细
		if cid, _ := m["companyId"].(string); cid != "" {
			if cid == "noedu-id" {
				return `{"code":100,"data":{"list":[{"entName":"无学历企业","alumni":[]}]}}`
			}
			return `{"code":100,"data":{"list":[{"entName":"安徽智侒信信息技术有限公司","alumni":[` +
				`{"alumniName":"李坤","position":"董事长","universityName":["哈尔滨工业大学"],"pId":"pid-likun"},` +
				`{"alumniName":"陈康康","position":"总经理","universityName":["哈尔滨工业大学"],"pId":"pid-chenkangkang"}]}]}}`
		}
		// 按学校筛校友圈：正式用法是 schoolName（文档权威参数，2026-09-17 由 type=1+text 校正）
		schoolName, _ := m["schoolName"].(string)
		if schoolName == "" {
			schoolName, _ = m["text"].(string) // 兼容旧写法（部分用例仍传 text）
		}
		if schoolName == "" || schoolName == "空结果大学" {
			return `{"code":100,"data":{"list":[]}}`
		}
		// 按 pageNo 返回不同企业，模拟真实分页（上游 pageNo 生效，page 无效）
		pageNo := 1
		if v, ok := m["pageNo"].(string); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				pageNo = n
			}
		}
		return fmt.Sprintf(`{"code":100,"data":{"total":99000,"list":[{"entName":"校友企业P%d","city":"合肥市",`+
			`"county":"蜀山区","entStatus":"存续","eid":"eid-%s-%d","alumni":[{"alumniName":"张三",`+
			`"position":"董事长","universityName":[%q]}]}]}}`, pageNo, schoolName, pageNo, schoolName)
	}
	return gwResponse
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "krmcp-mcp-audit")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: temp dir:", err)
		os.Exit(1)
	}
	sharedAuditPath = filepath.Join(dir, "audit.jsonl")

	sharedGW = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gwHits.Add(1)
		pcode := r.Header.Get("pcode")
		gwPcodes.mu.Lock()
		gwPcodes.last = append(gwPcodes.last, pcode)
		gwPcodes.mu.Unlock()
		fmt.Fprint(w, stubGatewayResponse(pcode, r))
	}))

	// 必须先于任何 audit.Default() / keeprisk.DefaultClient() 调用设置
	os.Setenv("KEEPRISK_API_BASE", sharedGW.URL)
	os.Setenv("KEEPRISK_AUDIT_PATH", sharedAuditPath)
	os.Setenv("KEEPRISK_AUDIT_ASYNC", "false")

	code := m.Run()
	sharedGW.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

// startTestServer 构造指向共享网关的 MCP server，返回客户端会话。
func startTestServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	kr, err := keeprisk.DefaultClient()
	if err != nil {
		t.Fatalf("网关客户端: %v", err)
	}
	qm := quota.New(quota.DefaultConfig())
	t.Cleanup(qm.Close)

	svc := &Services{
		Cat: cat, KR: kr, Quota: qm,
		Cache: cache.New(cache.Config{Enabled: true, TTL: 300 * time.Second, MaxSize: 100, PerAppCode: true}),
	}
	srv := NewServer(svc)

	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// AppCode 注入 Run 的 ctx：内存传输不经过 HTTP 鉴权中间件，
	// 但 SDK 由该 ctx 派生请求 ctx，等价于生产链路上中间件的效果。
	go func() { _ = srv.Run(auth.WithAppCode(ctx, "TestApp"), st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// auditLine 解析后的审计行（仅取断言需要的字段）
type auditLine struct {
	Method string         `json:"method"`
	Tool   string         `json:"tool"`
	App    string         `json:"app_code"`
	Pcode  string         `json:"pcode"`
	Status string         `json:"status"`
	Extra  map[string]any `json:"extra"`
}

// readAuditLines 读取自 offset 字节起的审计行（各测试用同一文件，按偏移切分）。
func readAuditLines(t *testing.T, offset int64) ([]auditLine, int64) {
	t.Helper()
	f, err := os.Open(sharedAuditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset
		}
		t.Fatalf("打开审计文件: %v", err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	var out []auditLine
	dec := json.NewDecoder(f)
	for {
		var l auditLine
		if err := dec.Decode(&l); err != nil {
			break
		}
		out = append(out, l)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return out, info.Size()
}

func auditSize(t *testing.T) int64 {
	t.Helper()
	info, err := os.Stat(sharedAuditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat: %v", err)
	}
	return info.Size()
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) 返回错误: %+v", name, res.Content)
	}
}

// ─────────── 端到端：聚合工具扇出逐 pcode 可见 + gateway_calls 汇总 ───────────

func TestE2EAggregateFanoutAuditedPerPcode(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)

	callTool(t, cs, "enterprise_extended_profile", map[string]any{"ent_name": "全景画像测试有限公司"})

	if got := gwHits.Load(); got != 9 {
		t.Fatalf("伪造网关收到 %d 次请求, want 9（enterprise_extended_profile 扇出 9）", got)
	}

	lines, _ := readAuditLines(t, offset)

	// 1) 网关层：9 个节各落一条，含 pcode 且可归因到工具
	var gwRecs []auditLine
	for _, l := range lines {
		if l.Method == audit.MethodGateway {
			gwRecs = append(gwRecs, l)
		}
	}
	if len(gwRecs) != 9 {
		t.Fatalf("网关层记录 = %d 条, want 9; recs=%+v", len(gwRecs), gwRecs)
	}
	byPcode := map[string]int{}
	for _, r := range gwRecs {
		if r.Tool != "enterprise_extended_profile" {
			t.Errorf("网关记录 tool = %q, want enterprise_extended_profile", r.Tool)
		}
		if r.Pcode == "" {
			t.Error("网关记录 pcode 为空：这正是修复前的缺口（聚合调用无法按接口归因）")
		}
		if r.Status != "ok" {
			t.Errorf("网关记录 status = %q, want ok", r.Status)
		}
		if r.App != "Test****" {
			t.Errorf("app_code = %q, want Test****（脱敏后）", r.App)
		}
		byPcode[r.Pcode]++
	}
	if len(byPcode) != 9 {
		t.Fatalf("不同 pcode 数 = %d, want 9; got=%v", len(byPcode), byPcode)
	}
	for pc, n := range byPcode {
		if n != 1 {
			t.Errorf("pcode %s 记录 %d 次, want 1", pc, n)
		}
	}

	// 2) 工具层：gateway_calls 汇总 = 实际网关调用数（计费单位还原依据）
	var toolRec *auditLine
	for i := range lines {
		if lines[i].Method == "tools/call" && lines[i].Tool == "enterprise_extended_profile" {
			toolRec = &lines[i]
		}
	}
	if toolRec == nil {
		t.Fatal("未找到 enterprise_extended_profile 的 tools/call 记录")
	}
	if toolRec.Extra == nil {
		t.Fatal("tools/call 记录缺少 extra.gateway_calls")
	}
	if v, ok := toolRec.Extra["gateway_calls"].(float64); !ok || int(v) != 9 {
		t.Fatalf("gateway_calls = %v, want 9", toolRec.Extra["gateway_calls"])
	}
}

// ─────────── 端到端：缓存命中不计网关调用（gateway_calls=0）───────────

func TestE2ECacheHitNotBilled(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)

	args := map[string]any{"ent_name": "缓存命中测试有限公司"}
	callTool(t, cs, "enterprise_basic_profile", args) // 首次：真实调用并写缓存
	callTool(t, cs, "enterprise_basic_profile", args) // 二次：命中缓存

	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关收到 %d 次请求, want 1（第二次应命中缓存）", got)
	}

	lines, _ := readAuditLines(t, offset)
	var gwCount, toolCount int
	var first, second any
	for _, l := range lines {
		switch l.Method {
		case audit.MethodGateway:
			if l.Tool == "enterprise_basic_profile" {
				gwCount++
			}
		case "tools/call":
			if l.Tool == "enterprise_basic_profile" && l.Extra != nil {
				if first == nil {
					first = l.Extra["gateway_calls"]
				}
				second = l.Extra["gateway_calls"]
				toolCount++
			}
		}
	}
	if gwCount != 1 {
		t.Fatalf("网关层记录 = %d, want 1（缓存命中不产生记录）", gwCount)
	}
	if toolCount != 2 {
		t.Fatalf("tools/call 记录 = %d, want 2", toolCount)
	}
	if v, ok := first.(float64); !ok || int(v) != 1 {
		t.Fatalf("首次调用 gateway_calls = %v, want 1", first)
	}
	if v, ok := second.(float64); !ok || int(v) != 0 {
		t.Fatalf("第二次调用 gateway_calls = %v, want 0（命中缓存不计费）", second)
	}
}

// ─────────── 端到端：开关关闭时无逐 pcode 明细，但 gateway_calls 仍在 ───────────

func TestE2EPerPcodeSwitchOffKeepsCount(t *testing.T) {
	t.Setenv("KEEPRISK_AUDIT_PER_PCODE", "false")
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)

	callTool(t, cs, "enterprise_basic_profile", map[string]any{"ent_name": "开关关闭测试有限公司"})

	var gwRecs int
	var gc any
	lines, _ := readAuditLines(t, offset)
	for _, l := range lines {
		if l.Method == audit.MethodGateway {
			gwRecs++
		}
		if l.Method == "tools/call" && l.Tool == "enterprise_basic_profile" && l.Extra != nil {
			gc = l.Extra["gateway_calls"]
		}
	}
	if gwRecs != 0 {
		t.Fatalf("开关关闭时网关层明细 = %d 条, want 0", gwRecs)
	}
	if v, ok := gc.(float64); !ok || int(v) != 1 {
		t.Fatalf("gateway_calls = %v, want 1（关闭明细不得丢失成本总量）", gc)
	}
	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关收到 %d 次请求, want 1（关闭审计不影响业务调用）", got)
	}
}

// ─────────── 稳定性：并发工具调用下计数器不串号 ───────────

func TestE2EConcurrentToolCallsCountersIsolated(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)

	const n = 6
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "enterprise_extended_profile",
				Arguments: map[string]any{"ent_name": fmt.Sprintf("并发示例科技%d有限公司", i)},
			})
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发 CallTool: %v", err)
		}
	}

	var counts []int
	lines, _ := readAuditLines(t, offset)
	for _, l := range lines {
		if l.Method == "tools/call" && l.Tool == "enterprise_extended_profile" && l.Extra != nil {
			if v, ok := l.Extra["gateway_calls"].(float64); ok {
				counts = append(counts, int(v))
			}
		}
	}
	if len(counts) != n {
		t.Fatalf("tools/call 记录 = %d, want %d; lines=%+v", len(counts), n, lines)
	}
	for i, c := range counts {
		if c != 9 {
			t.Fatalf("第 %d 条 gateway_calls = %d, want 9（并发下计数器必须请求级隔离）", i, c)
		}
	}
	if strings.Contains(fmt.Sprint(counts), "-") {
		t.Fatalf("计数出现负值: %v", counts)
	}
}
