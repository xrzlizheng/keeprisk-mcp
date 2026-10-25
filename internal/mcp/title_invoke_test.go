package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 名称直调链路端到端验证：search_keeprisk_apis 检索到的接口名称
// 必须能一路走到网关（解析出的 pcode 出现在网关请求头与审计记录中），
// 且歧义/未知名称在本地拒绝、不产生网关调用（不计费）。

// callToolRaw 调用工具并返回原始文本结果（不因 IsError 失败）
func callToolRaw(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// ─────────── 核心：名称直调走通到网关，解析出的 pcode 正确 ───────────

func TestE2EInvokeByTitle(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	// 院校信息：data1 模式按院校名查询（此前用企业名是错误用法，传企业名会被忽略并返回默认列表）
	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "院校信息",
		"params": map[string]any{"universityName": "哈尔滨工业大学"},
	})
	if strings.Contains(out, "error") {
		t.Fatalf("名称直调应成功, got: %s", out)
	}

	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关调用 %d 次, want 1", got)
	}
	after := gwPcodesSnapshot()
	if len(after) != len(before)+1 || after[len(after)-1] != "E_006_0011" {
		t.Fatalf("网关收到的 pcode = %v, 末位应为 E_006_0011（院校信息）", after)
	}

	// 审计记录：网关层 pcode 归因 + 工具层汇总
	lines, _ := readAuditLines(t, offset)
	var sawGateway bool
	for _, l := range lines {
		if l.Method == "gateway" && l.Tool == "keeprisk_query" && l.Pcode == "E_006_0011" {
			sawGateway = true
		}
	}
	if !sawGateway {
		t.Fatal("审计缺少 keeprisk_query→E_006_0011 的网关层记录")
	}
}

// 名称解析结果不外泄：对客输出中不得出现接口编码（脱敏兜底）
func TestE2EInvokeByTitleHidesPcodeInOutput(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "校企筛选V2",
		"params": map[string]any{"searchKey": "智侒信"},
	})
	if strings.Contains(out, "E_006_0021") {
		t.Fatal("对客输出泄露了接口编码（脱敏失效）")
	}
}

// ─────────── 歧义名称：本地拒绝，零网关调用 ───────────

func TestE2EAmbiguousTitleRejectedWithoutGatewayCall(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)

	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "筛选",
		"params": map[string]any{},
	})
	if !strings.Contains(out, "匹配到多个接口") {
		t.Fatalf("歧义名称应列出候选拒调, got: %s", out)
	}
	if got := gwHits.Load(); got != 0 {
		t.Fatalf("歧义名称不得触达网关, 实际 %d 次", got)
	}
}

// ─────────── 未知名称：本地拒绝，零网关调用 ───────────

func TestE2EUnknownTitleRejectedWithoutGatewayCall(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)

	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "不存在接口XYZ",
		"params": map[string]any{},
	})
	if !strings.Contains(out, "未知接口名称") {
		t.Fatalf("未知名称应明确报错, got: %s", out)
	}
	if got := gwHits.Load(); got != 0 {
		t.Fatalf("未知名称不得触达网关, 实际 %d 次", got)
	}
}

// ─────────── pcode 路径回归：重构后行为不变 ───────────

func TestE2EInvokeByPcodeStillWorks(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "E_007_0501",
		"params": map[string]any{"entName": "测试科技有限公司"},
	})
	if strings.Contains(out, "error") {
		t.Fatalf("持码直调应成功, got: %s", out)
	}
	after := gwPcodesSnapshot()
	if len(after) != len(before)+1 || after[len(after)-1] != "E_007_0501" {
		t.Fatalf("网关收到的 pcode = %v, 末位应为 E_007_0501", after)
	}
}

// ─────────── get_keeprisk_api / validate_keeprisk_params 支持名称 ───────────

func TestE2EGetAPIByTitle(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "get_keeprisk_api", map[string]any{"pcode": "校企统计"})
	if strings.Contains(out, "error") {
		t.Fatalf("按名称查详情应成功, got: %s", out)
	}
	if !strings.Contains(out, "校企统计") {
		t.Fatalf("详情应包含接口名称, got: %s", out)
	}
	if strings.Contains(out, "E_017_0091") {
		t.Fatal("详情输出泄露了接口编码（脱敏失效）")
	}
}

func TestE2EValidateParamsByTitle(t *testing.T) {
	cs := startTestServer(t)
	// 校企统计必填 name（required_one_of）；传名称+空参数应得到参数校验错误
	// 而非"未知接口名称"——证明名称解析已生效。
	out := callToolRaw(t, cs, "validate_keeprisk_params", map[string]any{
		"pcode":  "校企统计",
		"params": map[string]any{},
	})
	if strings.Contains(out, "未知接口名称") {
		t.Fatalf("已知名称不应报未知, got: %s", out)
	}
	if !strings.Contains(out, "\"valid\":false") {
		t.Fatalf("缺必填参数应 valid=false, got: %s", out)
	}
}
