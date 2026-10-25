package mcp

// 2026-09-20 全量实测修复的回归测试（补充集，主体：安徽智侒信信息技术有限公司）。
// 与 regression_20260920_test.go 互补：本文件只收那边未覆盖的断言——
// 告警注入行为、timeline 截断、recruitment type、actual_controller 语义、E_007_0601 页大小统一、
// anyOf 分支的属性识别。

import (
	"encoding/json"
	"strings"
	"testing"
)

// P1-E：appendArgWarnings 注入到 warnings 数组、错误响应不叠加。
func TestAppendArgWarnings(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"ent_name": map[string]any{"type": "string"}}}
	out := appendArgWarnings(schema, map[string]any{"ent_name": "X", "company_name": "X"},
		`{"tool":"t","warnings":["已有警告"]}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	ws := m["warnings"].([]any)
	if len(ws) != 2 || !strings.Contains(ws[1].(string), "ent_name") {
		t.Errorf("应追加 1 条告警，实际 %v", ws)
	}
	errOut := appendArgWarnings(schema, map[string]any{"company_name": "X"}, `{"error":"validation"}`)
	if errOut != `{"error":"validation"}` {
		t.Errorf("错误响应不应叠加告警，实际 %s", errOut)
	}
	// 全部参数合法时输出应原样返回
	same := appendArgWarnings(schema, map[string]any{"ent_name": "X"}, `{"ok":1}`)
	if same != `{"ok":1}` {
		t.Errorf("无未知参数不应改写输出，实际 %s", same)
	}
}

// P1-E：anyOf 分支（主体选择器 schema）里声明的属性不应告警。
func TestUnknownArgWarningsAnyOfBranch(t *testing.T) {
	anyOfSchema := map[string]any{
		"anyOf": []any{
			map[string]any{"properties": map[string]any{"ent_name": map[string]any{"type": "string"}}},
			map[string]any{"properties": map[string]any{"company_id": map[string]any{"type": "string"}}},
		},
	}
	if unknownArgWarnings(anyOfSchema, map[string]any{"ent_name": "X"}) != nil {
		t.Error("anyOf 分支声明的 ent_name 不应告警")
	}
	warns := unknownArgWarnings(anyOfSchema, map[string]any{"ent_name": "X", "company_name": "X"})
	if len(warns) != 1 || !strings.Contains(warns[0], "ent_name") {
		t.Errorf("company_name 应告警并指向 ent_name，实际 %v", warns)
	}
}

// P2-D：timeline 长值截断（超长 before/after 占实测响应 59% 字节）。
func TestTimelineTruncateAndUnchanged(t *testing.T) {
	long := strings.Repeat("经营范围长文本", 60) // 360 字
	got := truncateTimelineValue(long)
	if len([]rune(got)) >= len([]rune(long)) {
		t.Errorf("超长值应被截断：%d 字", len([]rune(got)))
	}
	if !strings.Contains(got, "截断") {
		t.Errorf("截断应带标记：%s", got[:50])
	}
	if truncateTimelineValue("短值") != "短值" {
		t.Error("短值不应截断")
	}
}

// P1：E_017_0131 招聘信息分析必须带 type=1（缺 type 实测恒 101）。
func TestRecruitmentSectionHasType(t *testing.T) {
	for _, sec := range investmentAnalysisSections {
		if sec.Key == "recruitment_analysis" {
			if v, ok := sec.ExtraParams["type"].(int); !ok || v != 1 {
				t.Errorf("recruitment_analysis 应带 type=1，实际 %v", sec.ExtraParams)
			}
			return
		}
	}
	t.Fatal("未找到 recruitment_analysis 节")
}

// P1（pageSize 统一）：E_007_0601 各调用点必须同页大小，否则缓存 key 分裂重复计费
// （实测同主体一回合被注入 5/10/20 三种、重复计费 3 次）。
func TestE0070601PageSizeUnified(t *testing.T) {
	const want = 20
	for _, sec := range extendedProfileSections {
		if sec.Pcode == "E_007_0601" {
			ps, _ := sec.ExtraParams["pageSize"].(int)
			if ps != want {
				t.Errorf("extended_profile 的 E_007_0601 pageSize 应为 %d，实际 %v", want, ps)
			}
		}
	}
	for _, sec := range dueDiligenceSections {
		if sec.Pcode == "E_007_0601" && sec.PageSize != want {
			t.Errorf("snapshot 的 E_007_0601 PageSize 应为 %d，实际 %d", want, sec.PageSize)
		}
	}
}

// P0-B：actual_controller 描述必须声明"向下/对外"语义（direction 字段在线上验证）。
func TestActualControllerDescDeclaresOutbound(t *testing.T) {
	props, _ := schemaActualController()["properties"].(map[string]any)
	ent, _ := props["ent_name"].(map[string]any)
	desc, _ := ent["description"].(string)
	if !strings.Contains(desc, "下游") && !strings.Contains(desc, "outbound") {
		t.Errorf("schema 应声明向下/outbound 语义，实际 %q", desc)
	}
}
