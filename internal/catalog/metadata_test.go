package catalog

import (
	"strings"
	"testing"
)

// 本文件守护「目录元数据修正层」（overrides.go）：
//   - 修正必须生效（required_one_of / 提示 / 示例）；
//   - 示例键名必须被归一（上游文档常带空格/大小写错乱）；
//   - "示例无法满足自身必填"的接口只允许清单内已知项（防止新增矛盾）。

func TestMetadataFixesApplied(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}

	t.Run("校企统计必填修正为 schoolName", func(t *testing.T) {
		e := c.GetByPcode("E_017_0091")
		if e == nil {
			t.Fatal("未找到 E_017_0091")
		}
		groups := requiredGroupsOf(e)
		if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0] != "schoolName" {
			t.Fatalf("required_one_of = %v, want [[schoolName]]", groups)
		}
		// input_schema 必须同步（get_keeprisk_api 输出原文，不一致会给模型自相矛盾的信息）
		if got := e.InputSchema["required_one_of"]; got == nil {
			t.Fatal("input_schema.required_one_of 缺失")
		} else if s := toGroups(got); len(s) != 1 || s[0][0] != "schoolName" {
			t.Fatalf("input_schema.required_one_of = %v, want [[schoolName]]", got)
		}
	})

	t.Run("产业链节点统计必填修正为 chainName", func(t *testing.T) {
		e := c.GetByPcode("E_007_1251")
		groups := requiredGroupsOf(e)
		if len(groups) != 1 || groups[0][0] != "chainName" {
			t.Fatalf("required_one_of = %v, want [[chainName]]", groups)
		}
	})

	t.Run("抽查检查接受 entName", func(t *testing.T) {
		e := c.GetByPcode("E_007_1601")
		groups := requiredGroupsOf(e)
		if len(groups) != 1 {
			t.Fatalf("required_one_of = %v", groups)
		}
		found := false
		for _, f := range groups[0] {
			if f == "entName" {
				found = true
			}
		}
		if !found {
			t.Fatalf("required_one_of = %v, 应包含 entName（运行时代码以 entName 调用）", groups)
		}
	})

	t.Run("校企筛选V2 示例与提示指向文档权威用法", func(t *testing.T) {
		e := c.GetByPcode("E_006_0021")
		// 2026-09-17 校正：此前基于试探得出的 "type=1+text 是校友圈入口" 被原始文档推翻——
		// type=1 实为"企业名称模糊搜索"；按学校筛校友圈应用 schoolName（或 searchKey.type=4）。
		// 示例现取自原始文档（含 searchKey 对象写法）。
		if !e.ExampleFromDoc {
			t.Fatal("示例应取自原始文档（权威来源）")
		}
		if _, ok := e.ExampleRequest["searchKey"]; !ok {
			t.Fatalf("文档示例应含 searchKey 对象写法: %v", e.ExampleRequest)
		}
		hints := map[string]string{}
		for _, p := range e.BusinessParams {
			hints[p.Name] = p.Hint
		}
		if !strings.Contains(hints["schoolName"], "推荐参数") {
			t.Errorf("schoolName 应标注为按学校查校友圈的推荐参数: %q", hints["schoolName"])
		}
		if !strings.Contains(hints["searchKey"], "对象") {
			t.Errorf("searchKey 应标注为对象类型: %q", hints["searchKey"])
		}
		if !strings.Contains(hints["type"], "4=学校名称") {
			t.Errorf("type 应说明 4=学校名称: %q", hints["type"])
		}
		// 文档列出的枚举参数应被补充进参数表
		for _, name := range []string{"labelList", "entStatusList", "scaleList"} {
			if _, ok := hints[name]; !ok {
				t.Errorf("文档参数 %s 未被补充进参数表", name)
			}
		}
	})

	t.Run("院校信息双模式提示（data1 院校详情 / data2 企业+高校标签）", func(t *testing.T) {
		e := c.GetByPcode("E_006_0011")
		hints := map[string]string{}
		for _, p := range e.BusinessParams {
			hints[p.Name] = p.Hint
		}
		// 2026-09-17 依据 PDF 文档 + 实测校正：本接口是**院校信息查询**（非"榜单"），
		// 双模式——universityName 返回院校详情（tags/简介/类型/logo），province 返回企业+高校标签。
		if !strings.Contains(hints["universityName"], "data1") || !strings.Contains(hints["universityName"], "标签") {
			t.Errorf("universityName 应说明 data1 模式与院校详情（含标签）: %q", hints["universityName"])
		}
		if !strings.Contains(hints["province"], "data2") || !strings.Contains(hints["province"], "企业") {
			t.Errorf("province 应说明 data2 模式（企业列表+高校标签）: %q", hints["province"])
		}
		// 目录误列的 entName 必须标注为非本接口参数
		for _, p := range e.BusinessParams {
			if p.Name == "entName" && !strings.Contains(p.Hint, "非本接口参数") {
				t.Fatalf("entName 应标注为非本接口参数（目录误列）: %q", p.Hint)
			}
		}
	})
}

// toGroups 把任意形态的 required_one_of 解成 [][]string（测试断言用）
func toGroups(v any) [][]string {
	out := [][]string{}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if g, ok := item.([]any); ok {
				row := []string{}
				for _, x := range g {
					row = append(row, x.(string))
				}
				out = append(out, row)
			}
		}
	}
	return out
}

func TestExampleKeysNormalized(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}
	// E_010_0271 上游示例键为 ' companyname '（带空格、小写）→ 应归一为 companyName
	e := c.GetByPcode("E_010_0271")
	if e == nil {
		t.Fatal("未找到 E_010_0271")
	}
	for k := range e.ExampleRequest {
		if k != strings.TrimSpace(k) {
			t.Fatalf("示例键仍带首尾空格: %q", k)
		}
	}
	if _, ok := e.ExampleRequest["companyName"]; !ok {
		t.Fatalf("示例键未归一为 companyName: %v", e.ExampleRequest)
	}
}

// knownExampleGaps 是「示例仅含可选参数、未含必填主体」的已知接口。
// 这些示例本身是筛选项片段（非可直接照抄的完整入参），由 validate 警告兜底；
// 新增此类问题必须显式登记，防止元数据矛盾悄悄回归。
var knownExampleGaps = map[string]string{
	"E_007_0701": "示例仅含地区筛选项（分支机构）",
	"E_007_0801": "示例仅含时间范围（变更信息）",
	"E_017_0191": "示例仅含地区筛选项（对外投资V2）",
	"E_017_0211": "示例仅含时间范围（购地信息分析）",
	"E_007_1221": "示例为检索条件组合（科研线索招商）",
	"E_007_1421": "示例主体字段与 required_one_of 不一致（上市公司主要人员，待上游确认）",
}

func TestExampleSatisfiesRequired(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}
	satisfied := func(ex map[string]any, field string) bool {
		if v, ok := ex[field]; ok && v != nil && v != "" {
			return true
		}
		if field == "name" { // 与 params.hasRequiredOneOfValue 口径一致
			for _, alt := range []string{"entName", "companyName"} {
				if v, ok := ex[alt]; ok && v != nil && v != "" {
					return true
				}
			}
		}
		return false
	}
	var violations []string
	items := c.LatestAPIs()
	for i := range items {
		e := &items[i]
		if len(e.ExampleRequest) == 0 {
			continue
		}
		groups := requiredGroupsOf(e)
		gapped := false
		for _, g := range groups {
			ok := false
			for _, f := range g {
				if satisfied(e.ExampleRequest, f) {
					ok = true
					break
				}
			}
			if !ok {
				gapped = true
			}
		}
		if gapped {
			if _, known := knownExampleGaps[e.Pcode]; !known {
				violations = append(violations, e.Pcode+" "+e.Title)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("出现新的「示例无法满足必填」接口（元数据矛盾回归）：%v\n"+
			"修复方式：catalog/overrides.go 修正元数据，或登记到 knownExampleGaps 并说明原因", violations)
	}
}

func TestSynonymsRouteAlumni(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}
	// 2026-09-16 盲测：关键词"校友"无同义词路由 → 检索为空
	cases := map[string]string{
		"校友": "校企筛选V2",
		"母校": "校企筛选V2",
		"高校": "院校信息",
		"大学": "院校信息",
		// 2026-09-16 二轮盲测：模型按"高管/董监高"检索，漏判标题特殊的「查老板」
		"老板": "查老板",
		"高管": "查老板",
		"董监高": "查老板",
	}
	for q, wantTitle := range cases {
		hits := c.Search(q, "", "", 10)
		found := false
		for _, h := range hits {
			if h.Title == wantTitle {
				found = true
			}
		}
		if !found {
			titles := make([]string, 0, len(hits))
			for _, h := range hits {
				titles = append(titles, h.Title)
			}
			t.Fatalf("检索 %q 未命中 %s; 实际=%v", q, wantTitle, titles)
		}
	}
}
