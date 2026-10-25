package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// 入口工具的参数兼容与富错误（2026-09-16 二轮盲测：模型按其他工具惯例传 ent_name，
// 被 SDK schema 硬拒为 -32602 且无业务语义，占硬性试错的大头）。
//
// 设计取舍：入口工具不在 JSON Schema 声明 required（SDK 会硬拒、无法自定义文案），
// 改为 handler 内做别名归一 + 富错误；下方用例锁定该行为。

func TestPickStrArgAliases(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"规范名", map[string]any{"keyword": "华为"}, "华为"},
		{"ent_name 别名", map[string]any{"ent_name": "华为"}, "华为"},
		{"company_name 别名", map[string]any{"company_name": "华为"}, "华为"},
		{"name 别名", map[string]any{"name": "华为"}, "华为"},
		{"规范名优先于别名", map[string]any{"keyword": "规范", "ent_name": "别名"}, "规范"},
		{"首尾空格清理", map[string]any{"ent_name": "  华为  "}, "华为"},
		{"空串视为缺失", map[string]any{"ent_name": "   "}, ""},
		{"非字符串不误取", map[string]any{"ent_name": map[string]any{"a": 1}}, ""},
		{"nil 参数", nil, ""},
		{"全部缺失", map[string]any{"region": "广东"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickStrArg(tc.args, "keyword"); got != tc.want {
				t.Fatalf("pickStrArg = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPickStrArgPersonAliases(t *testing.T) {
	if got := pickStrArg(map[string]any{"name": "陈康康"}, "person_name"); got != "陈康康" {
		t.Fatalf("name 应作为 person_name 别名, got %q", got)
	}
	if got := pickStrArg(map[string]any{"person_name": "陈康康"}, "person_name"); got != "陈康康" {
		t.Fatalf("规范名优先, got %q", got)
	}
}

// 富错误必须含：业务语义、可接受的字段名、可照抄的示例、下一步指引。
func TestMissingEntryArgRichError(t *testing.T) {
	out := missingEntryArg(entryArgSpec{
		Tool: "resolve_entity", Canonical: "keyword",
		Meaning: "企业名称关键词（全称/简称/曾用名均可）",
		Example: map[string]any{"keyword": "安徽智侒信信息技术有限公司"},
		Hint:    "也可用 fuzzy_search。",
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("错误输出非 JSON: %v; %s", err, out)
	}
	if m["error"] != "validation" {
		t.Fatalf("error = %v, want validation", m["error"])
	}
	msg, _ := m["message"].(string)
	if !strings.Contains(msg, "keyword") || !strings.Contains(msg, "企业名称") {
		t.Fatalf("message 应含参数名与业务语义: %q", msg)
	}
	fields, _ := m["accepted_fields"].([]any)
	if len(fields) < 2 {
		t.Fatalf("accepted_fields 应列出规范名与别名: %v", m["accepted_fields"])
	}
	if m["example"] == nil {
		t.Fatal("应给出可照抄的 example")
	}
	if m["hint"] == nil {
		t.Fatal("应给出下一步 hint")
	}
}

// ─────────── 端到端：别名与富错误在真实 MCP 链路上生效 ───────────

func TestE2EEntryArgAliasAccepted(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	// 模型惯用 ent_name 调 resolve_entity（规范名是 keyword）——首调即成功
	out := callToolRaw(t, cs, "resolve_entity", map[string]any{"ent_name": "安徽智侒信信息技术有限公司"})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("别名应被接受: %s", out)
	}
	if !strings.Contains(out, "candidates") {
		t.Fatalf("应返回候选列表: %s", out)
	}
	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关调用 = %d, want 1", got)
	}
	after := gwPcodesSnapshot()
	if len(after) != len(before)+1 {
		t.Fatalf("应恰好新增 1 次网关调用: %v", after[len(before):])
	}
	_ = offset
}

func TestE2EMissingEntryArgRichErrorOnWire(t *testing.T) {
	cs := startTestServer(t)
	before := len(gwPcodesSnapshot())

	out := callToolRaw(t, cs, "resolve_entity", map[string]any{})
	if !strings.Contains(out, "keyword") {
		t.Fatalf("缺参应提示规范参数名: %s", out)
	}
	if !strings.Contains(out, "example") {
		t.Fatalf("缺参应给出可照抄示例: %s", out)
	}
	// 缺参不得触达网关（不计费）
	if len(gwPcodesSnapshot()) != before {
		t.Fatal("缺参不应触达网关")
	}
}

func TestE2EPersonRiskProfileAliasAccepted(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "person_risk_profile", map[string]any{"name": "陈康康"})
	if strings.Contains(out, `"error"`) && strings.Contains(out, "person_name") {
		t.Fatalf("name 应作为 person_name 别名被接受: %s", out)
	}
}

// ─────────── 翻页参数曾静默无效 → 现为真实分页（历史用例语义已更新）───────────
//
// 2026-09-16 二轮盲测指出 `page` 被静默忽略（危险：单页会被当全量）；
// 2026-09-17 复审发现上游其实支持**pageNo** 分页，`page` 才是无效名。
// 现 `page` 为起始页码（映射到上游 pageNo），行为见 school_tools_test.go 的分页用例。

func TestWithWarningAppendsAndDedups(t *testing.T) {
	base := `{"a":1}`
	once := withWarning(base, "w1")
	twice := withWarning(once, "w2")
	var m map[string]any
	if err := json.Unmarshal([]byte(twice), &m); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, twice)
	}
	ws, _ := m["_warnings"].([]any)
	if len(ws) != 2 {
		t.Fatalf("_warnings 应累积两条: %v", ws)
	}
	// 非 JSON 输入原样返回（不破坏既有输出）
	if got := withWarning("not json", "w"); got != "not json" {
		t.Fatalf("非 JSON 应原样返回, got %q", got)
	}
}
