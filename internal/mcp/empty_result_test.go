package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// 无数据（code=110 / 空 data）是**正常的成功响应**，必须显式标注，
// 避免模型把 data:null 读成失败。本文件锁定该行为。
//
// 用户反馈（2026-09-17）："网关接口有时是无数据，这是正常的响应，也是接口正确调用的结果，
// 因为有的查询结果就是无数据，这个不能视为错误！"

func TestIsEmptyResult(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{"code=110（网关明示无数据）", map[string]any{"code": "110", "data": nil}, true},
		{"code=110 数字形态", map[string]any{"code": float64(110), "data": nil}, true},
		{"code=100 且 data 为 null", map[string]any{"code": "100", "data": nil}, true},
		{"code=100 且 data 空 map", map[string]any{"code": "100", "data": map[string]any{}}, true},
		{"code=100 且 list 空 total=0", map[string]any{"code": "100",
			"data": map[string]any{"list": []any{}, "total": float64(0)}}, true},
		{"code=100 且有数据", map[string]any{"code": "100",
			"data": map[string]any{"list": []any{map[string]any{"a": 1}}, "total": float64(1)}}, false},
		{"错误响应（error 字段）不算无数据", map[string]any{"error": "api", "code": "500", "data": nil}, false},
		{"code=500 不算无数据", map[string]any{"code": "500", "data": nil}, false},
		{"code=101 参数错误不算", map[string]any{"code": "101", "data": nil}, false},
		{"code=107 未开通不算", map[string]any{"code": "107", "data": nil}, false},
		{"无 code 字段不算", map[string]any{"data": nil}, false},
		{"nil 输入", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyResult(tc.in); got != tc.want {
				t.Fatalf("isEmptyResult = %v, want %v; in=%v", got, tc.want, tc.in)
			}
		})
	}
}

func TestCodeOf(t *testing.T) {
	cases := map[any]int{
		"110": 110, "100": 100, float64(110): 110, 110: 110,
		"abc": 0, "": 0, nil: 0, "12a": 0,
	}
	for in, want := range cases {
		if got := codeOf(in); got != want {
			t.Errorf("codeOf(%v) = %d, want %d", in, got, want)
		}
	}
}

// 关键：错误响应**绝不能**被标注为"正常无数据"（否则会把故障伪装成结论）
func TestAnnotateDoesNotMarkErrors(t *testing.T) {
	errs := []string{
		`{"error":"api","message":"code=500 未知请求错误","code":"500"}`,
		`{"error":"validation","errors":["缺少必填字段: entName"]}`,
		`{"error":"quota","message":"超出配额"}`,
	}
	for _, in := range errs {
		out := annotateEmptyResults(in)
		if strings.Contains(out, "_empty_result") {
			t.Fatalf("错误响应被误标为无数据: %s", out)
		}
	}
}

func TestAnnotateTopLevelEmpty(t *testing.T) {
	in := `{"code":"110","data":null,"message":"查询成功，无数据","charge":false}`
	out := annotateEmptyResults(in)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	if m["_empty_result"] != true {
		t.Fatalf("应标注 _empty_result=true: %s", out)
	}
	// 说明文案可能来自两层：QueryImpl 的 _note（详细）或统一出口的 _empty_note（简短）
	note, _ := m["_empty_note"].(string)
	if note == "" {
		note, _ = m["_note"].(string)
	}
	if !strings.Contains(note, "正常") ||
		(!strings.Contains(note, "非错误") && !strings.Contains(note, "不是错误")) {
		t.Fatalf("说明应明确'正常/非错误': %q", note)
	}
	// 必须保留原始字段（不改写业务数据）
	if m["code"] != "110" || m["message"] != "查询成功，无数据" {
		t.Fatal("不应改动原始响应字段")
	}
}

// 聚合工具分节：嵌套的 data.code=110 也要被标注，并给顶层总结
func TestAnnotateNestedSections(t *testing.T) {
	in := `{
	  "ent_name":"测试企业",
	  "ok_count":2,
	  "sections":[
	    {"key":"basic","status":"ok","data":{"code":"100","data":{"entName":"测试企业"}}},
	    {"key":"enforced","status":"ok","data":{"code":"110","data":null}},
	    {"key":"dishonest","status":"ok","data":{"code":"110","data":null}}
	  ]
	}`
	out := annotateEmptyResults(in)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	secs, _ := m["sections"].([]any)
	if len(secs) != 3 {
		t.Fatalf("分节数 = %d, want 3", len(secs))
	}
	emptyCount := 0
	for _, s := range secs {
		sm, _ := s.(map[string]any)
		d, _ := sm["data"].(map[string]any)
		if d != nil && d["_empty_result"] == true {
			emptyCount++
			if _, ok := d["_empty_note"]; !ok {
				t.Fatalf("无数据分节应有 _empty_note: %v", d)
			}
		}
	}
	if emptyCount != 2 {
		t.Fatalf("应标注 2 个无数据分节，实际 %d", emptyCount)
	}
	// 有数据的分节不应被标注
	basicData, _ := ((secs[0].(map[string]any))["data"]).(map[string]any)
	if basicData["_empty_result"] != nil {
		t.Fatal("有数据的分节不应被标注")
	}
	// 顶层总结
	hint, _ := m["_empty_hint"].(string)
	if !strings.Contains(hint, "2 个维度无记录") {
		t.Fatalf("顶层总结应统计无数据维度数: %q", hint)
	}
}

// 无数据不需标注时原样返回（避免无谓改写 JSON）
func TestAnnotateNoOpWhenHasData(t *testing.T) {
	in := `{"code":"100","data":{"entName":"某公司","list":[{"a":1}]}}`
	out := annotateEmptyResults(in)
	if strings.Contains(out, "_empty_result") {
		t.Fatalf("有数据不应标注: %s", out)
	}
	if out != in {
		t.Fatalf("无改动时应原样返回；out=%s", out)
	}
}

// 非 JSON 输入原样返回
func TestAnnotateNonJSON(t *testing.T) {
	in := "not json at all"
	if got := annotateEmptyResults(in); got != in {
		t.Fatalf("非 JSON 应原样返回, got %q", got)
	}
}

// ─────────────── items 形态载荷的判空回归（2026-09-18 飞友科技全量实测）───────────────
//
// 背景：旧 isEmptyResult 只识别 `data.list`，对载荷键为 `items` 的接口有两个漏判：
//  1. 完全认不出 items 数组；
//  2. 这类接口的内层载荷节点形如 {code:100, items:[...], total:N}，**自身没有 data 键**，
//     旧实现的 `switch m["data"]` 命中 `case nil` → 直接判空。
//
// 实测后果（飞友科技有限公司）：E_003_0601 法院公告 / E_003_0901 开庭公告 /
// E_007_0201 实际控制权 / E_007_0301 最终受益人 四类接口带 2~5 条 items 却被标
// `_empty_result:true` + "此结论可采信，无需重试"——其中法院公告是唯一的红级风险来源，
// 会直接导致"无风险记录"的错误结论。
//
// 依据（接口文档，/接口文档pdf/markdown/企业/）：
//   E_003_0901 开庭公告 data 节点说明：`total 数据总数` / `items 集合`
//   E_007_0201 实际控制权 data 节点说明：`total 总条数`   / `items 集合`
//   E_007_0301 最终受益人 data 节点说明：`total 总条数`   / `items 集合`
// 三者的官方返回示例均为 items 非空配 total，且 E_007_0201 示例本身即 total=268 + 10 条 items
// （印证 total=总量、items=单页明细，两者不等属正常分页，不是缺陷）。
func TestIsEmptyResultItemsShape(t *testing.T) {
	// 真实形态：外层信封 + 内层载荷节点（内层无 data 键）
	innerPayload := map[string]any{
		"code":    float64(100),
		"message": "查询成功",
		"total":   float64(2),
		"items":   []any{map[string]any{"caseNo": "（2021）鲁71民初70号"}, map[string]any{"caseNo": ""}},
	}
	full := map[string]any{
		"charge":  true,
		"code":    "100",
		"message": "查询成功",
		"orderNo": "123",
		"data":    innerPayload,
	}

	if isEmptyResult(innerPayload) {
		t.Error("内层载荷节点（无 data 键、items 非空）不应被判为空集")
	}
	if isEmptyResult(full) {
		t.Error("完整响应（items 非空）不应被判为空集")
	}

	// 判空不得回归：真空集仍须判空
	empties := []map[string]any{
		{"code": "110", "data": nil, "charge": false, "message": "查询成功，无数据"},
		{"code": "100", "data": nil},
		{"code": "100", "data": map[string]any{}},
		{"code": "100", "data": map[string]any{"list": []any{}, "total": float64(0)}},
		{"code": "100", "data": map[string]any{
			"code": float64(100), "message": "查询成功", "total": float64(0), "items": []any{}}},
	}
	for i, m := range empties {
		if !isEmptyResult(m) {
			t.Errorf("真空集 #%d 应判空: %v", i, m)
		}
	}
}

// 占位空壳（全 "-"）应判为空集：E_007_0941 企业财务年报指标 2022 期实测返回
// 15 个字段全为 "-" 的空对象，若不识别占位符会被读成"各项指标均为空/异常"。
func TestIsEmptyResultPlaceholderShell(t *testing.T) {
	shell := map[string]any{
		"code": float64(100), "message": "查询成功",
		"data": map[string]any{
			"assetLiRate": "-", "equityMult": "-", "equityRate": "-",
			"grossProfitRate": "-", "profitRate": "-", "taxBurdenRate": "-",
		},
	}
	if !isEmptyResult(shell) {
		t.Error("全占位符（\"-\"）空壳应判为空集")
	}
	// 有任一真实值则不算空
	real := map[string]any{
		"code": float64(100),
		"data": map[string]any{"assetLiRate": "73.07%", "equityMult": "-"},
	}
	if isEmptyResult(real) {
		t.Error("含真实值不应判为空集")
	}
}

// 扁平业务对象（无数组、无 total）不应被判为空：如实控权/标签/比值类接口。
func TestIsEmptyResultFlatObject(t *testing.T) {
	flat := map[string]any{
		"code": "100",
		"data": map[string]any{"entName": "飞友科技有限公司", "legalName": "郑洪峰", "entStatus": "存续"},
	}
	if isEmptyResult(flat) {
		t.Error("扁平业务对象（仅字符串字段）不应判为空集")
	}
	// 分页控制字段不得被当作载荷证据
	onlyPaging := map[string]any{
		"code": "100",
		"data": map[string]any{"pageNo": float64(1), "pageSize": float64(10), "total": float64(0)},
	}
	if !isEmptyResult(onlyPaging) {
		t.Error("仅含分页控制字段应判为空集（pageNo/pageSize 不得作为载荷证据）")
	}
}

// 框架自身附加的元数据字段不得被当作业务载荷。
//
// 实测（2026-09-18 annual_report_investments.financial_annual）：QueryImpl 会为每个响应
// 附加 `pcode_used`（接口编码字符串），若把它算作载荷证据，则 `code=110, data=null`
// 的真空响应会被判成 has_data=true —— 摘要显示"0 条记录"却标"有数据"，自相矛盾。
func TestHasAnyDataIgnoresFrameworkMetadata(t *testing.T) {
	// 真实形态：QueryImpl 输出（已附加 pcode_used / _empty_* / warnings）
	envelope := map[string]any{
		"pcode_used":    "E_007_0931",
		"_cache_hit":    true,
		"_empty_note":   "查询成功但无记录（正常成功响应，非错误、非故障）；此结论可采信，无需重试或改参",
		"_empty_result": true,
		"_next":         []any{"用 resolve_entity 或 enterprise_basic_profile 确认主体是否存在"},
		"charge":        false,
		"code":          "110",
		"data":          nil,
		"message":       "查询成功，无数据",
		"orderNo":       "1550564952335196160",
		"param":         nil,
		"time":          "1789725092839",
		"warnings":      []any{"某提示"},
	}
	if hasAnyData(envelope) {
		t.Error("真空响应（含框架附加的 pcode_used）不应被判为有数据")
	}
	if !isEmptyResult(envelope) {
		t.Error("真空响应应被判为空集")
	}

	// 有数据时仍须判为有数据（防止过度排除）
	withData := map[string]any{
		"pcode_used": "E_007_0931",
		"code":       "100",
		"charge":     true,
		"data":       map[string]any{"total_assets": "10000,20000", "retained_profit": "500,600"},
	}
	if !hasAnyData(withData) {
		t.Error("有业务载荷时应判为有数据")
	}
}
