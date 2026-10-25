package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ─────────────── 单元：校友学历二次校验（上游 text 过滤不严格的兜底）───────────────

func TestMatchAlumniEnterprises(t *testing.T) {
	list := []any{
		map[string]any{"entName": "甲企业", "city": "合肥市", "entStatus": "存续", "alumni": []any{
			map[string]any{"alumniName": "张三", "position": "董事长", "universityName": []any{"中国科学技术大学", "清华大学"}},
		}},
		map[string]any{"entName": "乙企业", "city": "北京市", "alumni": []any{
			map[string]any{"alumniName": "李四", "position": "董事", "universityName": []any{"北京大学"}},
		}},
		map[string]any{"entName": "丙企业", "alumni": []any{}},
		map[string]any{"entName": "丁企业", "city": "上海市", "alumni": []any{
			map[string]any{"alumniName": "王五", "position": "总经理", "universityName": []any{" 中国科学技术大学 "}},
		}},
	}

	t.Run("只保留确含该校学历的企业", func(t *testing.T) {
		matches, total := matchAlumniEnterprises(list, "中国科学技术大学")
		if len(matches) != 2 {
			t.Fatalf("命中企业 = %d, want 2（乙/丙应被排除）; matches=%v", len(matches), matches)
		}
		if total != 2 {
			t.Fatalf("命中校友 = %d, want 2", total)
		}
		names := []string{matches[0]["ent_name"].(string), matches[1]["ent_name"].(string)}
		for _, n := range names {
			if n != "甲企业" && n != "丁企业" {
				t.Fatalf("出现非目标企业: %v", names)
			}
		}
	})

	t.Run("简称不匹配（不做模糊推断，避免混入非目标院校）", func(t *testing.T) {
		matches, total := matchAlumniEnterprises(list, "中科大")
		if len(matches) != 0 || total != 0 {
			t.Fatalf("简称不应命中，matches=%d total=%d", len(matches), total)
		}
	})

	t.Run("空校名返回空", func(t *testing.T) {
		if m, n := matchAlumniEnterprises(list, "  "); m != nil || n != 0 {
			t.Fatalf("空校名应返回空, got %v %d", m, n)
		}
	})
}

// ─────────────── 单元：简称防护（全称判定 + 0 命中提示）───────────────

func TestLooksLikeFullName(t *testing.T) {
	full := []string{
		"中国科学技术大学", "清华大学", "北京电子科技职业学院", "合肥学院", "中国科学院",
	}
	abbr := []string{"中科大", "清华", "北大", "浙大", "上交", "", "Fudan"}
	for _, s := range full {
		if !looksLikeFullName(s) {
			t.Errorf("looksLikeFullName(%q) = false, want true", s)
		}
	}
	for _, s := range abbr {
		if looksLikeFullName(s) {
			t.Errorf("looksLikeFullName(%q) = true, want false（应触发简称解析）", s)
		}
	}
}

func TestZeroMatchHintIsActionable(t *testing.T) {
	// 0 命中绝不能被读成"该校没有校友企业"：两种情况都必须给出下一步动作
	upstreamEmpty := zeroMatchHint("某某大学", 0)
	if !strings.Contains(upstreamEmpty, "院校信息") || !strings.Contains(upstreamEmpty, "核对") {
		t.Fatalf("上游无候选时未给出校名核对指引: %s", upstreamEmpty)
	}
	filteredOut := zeroMatchHint("某某大学", 100)
	if !strings.Contains(filteredOut, "max_pages") || !strings.Contains(filteredOut, "校企筛选V2") {
		t.Fatalf("已扫描但无命中时未给出排查路径: %s", filteredOut)
	}
}

// ─────────────── 企业入口：企业 → 法人 → 院校 → 校友圈（二轮盲测发现）───────────────
//
// 背景：首版 _note 断言"无法由企业名反查院校"，被三个独立盲测代理指为最大绕路源。
// 实测「校企筛选V2」companyId 维度可返回企业人员学历，故新增 company_name 入口。

func TestE2ECompanyEntryFullChain(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"company_name": "安徽智侒信信息技术有限公司",
	})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("企业入口应成功: %s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	// 应自动由法人院校（哈尔滨工业大学）得到校友圈结果
	if parsed["school"] != "哈尔滨工业大学" {
		t.Fatalf("school = %v, want 哈尔滨工业大学（应由法人院校解析得到）", parsed["school"])
	}
	via, _ := parsed["via_company"].(string)
	if !strings.Contains(via, "陈康康") || !strings.Contains(via, "哈尔滨工业大学") {
		t.Fatalf("via_company 应说明推导链（企业→法人→院校）: %q", via)
	}

	// 调用链：工商基本信息 → 校企筛选V2(companyId) → 校企筛选V2(text)
	after := gwPcodesSnapshot()
	got := after[len(before):]
	want := []string{pcodeBasicProfile, pcodeSchoolFilter, pcodeSchoolFilter}
	if len(got) != len(want) {
		t.Fatalf("网关调用 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("调用序列 = %v, want %v", got, want)
		}
	}

	// 成本可见：3 次
	lines, _ := readAuditLines(t, offset)
	var gc any
	for _, l := range lines {
		if l.Method == "tools/call" && l.Tool == "school_alumni_circle" && l.Extra != nil {
			gc = l.Extra["gateway_calls"]
		}
	}
	if v, ok := gc.(float64); !ok || int(v) != 3 {
		t.Fatalf("gateway_calls = %v, want 3", gc)
	}
}

func TestE2ECompanyEntryNoUniversityFallback(t *testing.T) {
	cs := startTestServer(t)

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{"company_name": "无学历企业"})
	if !strings.Contains(out, "no_university_for_legal_person") {
		t.Fatalf("法人无学历时应给出结构化兜底（而非空结果）: %s", out)
	}
	if !strings.Contains(out, "legal_person") {
		t.Fatalf("兜底响应应包含法人信息: %s", out)
	}
	if !strings.Contains(out, "university_name") {
		t.Fatalf("兜底响应应指引改用 university_name 入口: %s", out)
	}
}

func TestE2ECompanyEntryUnknownCompany(t *testing.T) {
	cs := startTestServer(t)
	before := len(gwPcodesSnapshot())

	// 双入口都为空 → 本地校验拒绝（不触达网关）
	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{})
	if !strings.Contains(out, "university_name") || !strings.Contains(out, "company_name") {
		t.Fatalf("双入口都为空时应提示二选一: %s", out)
	}
	if len(gwPcodesSnapshot()) != before {
		t.Fatal("本地校验失败不应触达网关")
	}
}

// ─────────────── 端到端：简称自动解析 ───────────────

func TestE2EAbbreviationAutoResolved(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{"university_name": "清华"})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("简称应自动解析后成功: %s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	if parsed["school"] != "清华大学" {
		t.Fatalf("school = %v, want 清华大学（简称应被解析）", parsed["school"])
	}
	if parsed["school_input"] != "清华" {
		t.Fatalf("school_input = %v, want 清华（保留原始输入以便追溯）", parsed["school_input"])
	}
	if _, ok := parsed["_resolution"]; !ok {
		t.Fatal("缺少 _resolution（应说明已做简称解析）")
	}
	if got := parsed["matched_enterprises"].(float64); int(got) != 1 {
		t.Fatalf("matched_enterprises = %v, want 1（解析后应能命中）", got)
	}

	// 调用序列：院校信息（解析）→ 校企筛选（查询）
	after := gwPcodesSnapshot()
	if len(after) != len(before)+2 {
		t.Fatalf("网关调用数 = %d, want 2（解析 + 筛选）; pcode=%v", len(after)-len(before), after)
	}
	if after[len(after)-2] != pcodeSchoolInfo || after[len(after)-1] != pcodeSchoolFilter {
		t.Fatalf("调用序列 = %v, want [%s %s]", after, pcodeSchoolInfo, pcodeSchoolFilter)
	}

	// 审计：两次网关调用均计入（成本可见）
	lines, _ := readAuditLines(t, offset)
	var gc any
	for _, l := range lines {
		if l.Method == "tools/call" && l.Tool == "school_alumni_circle" && l.Extra != nil {
			gc = l.Extra["gateway_calls"]
		}
	}
	if v, ok := gc.(float64); !ok || int(v) != 2 {
		t.Fatalf("gateway_calls = %v, want 2（简称解析的成本必须可见）", gc)
	}
}

func TestE2EUnresolvableAbbreviationErrorsNotSilentEmpty(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{"university_name": "无法解析"})
	if !strings.Contains(out, "不是院校全称") {
		t.Fatalf("解析失败应明确报错而非返回空结果: %s", out)
	}
	if strings.Contains(out, `"matched_enterprises":0`) {
		t.Fatal("不得返回 0 命中（会被误读为该校无校友企业）")
	}
	// 解析失败即返回：不跑注定 0 命中的筛选（成本纪律）
	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关调用 = %d, want 1（仅解析尝试）", got)
	}
}

func TestE2EZeroMatchHintInProduction(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{"university_name": "空结果大学"})
	if !strings.Contains(out, "_hint") {
		t.Fatalf("0 命中必须给出 _hint: %s", out)
	}
	if !strings.Contains(out, "院校信息") {
		t.Fatalf("_hint 应给出校名核对路径: %s", out)
	}
}

// ─────────── 分页：pageNo 生效（2026-09-17 实测修正）───────────
//
// 此前误判"上游不支持翻页"（试的是无效参数名 page），实际「校企筛选V2」支持 pageNo。
// 教训：与"无法由企业名反查院校"同类——用错误参数名的失败否定整个能力。

func TestE2EPaginationScansMultiplePages(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"university_name": "清华大学", "page_size": 1, "max_pages": 3,
	})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("分页调用应成功: %s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	// 3 页 → 3 次网关调用
	if got := gwHits.Load(); got != 3 {
		t.Fatalf("网关调用 = %d, want 3（3 页各 1 次）", got)
	}
	if p := int(parsed["pages_scanned"].(float64)); p != 3 {
		t.Fatalf("pages_scanned = %d, want 3", p)
	}
	if _, ok := parsed["_sampling"]; !ok {
		t.Fatal("应返回 _sampling 说明扫描范围")
	}
	// 成本可见
	lines, _ := readAuditLines(t, offset)
	var gc any
	for _, l := range lines {
		if l.Method == "tools/call" && l.Tool == "school_alumni_circle" && l.Extra != nil {
			gc = l.Extra["gateway_calls"]
		}
	}
	if v, ok := gc.(float64); !ok || int(v) != 3 {
		t.Fatalf("gateway_calls = %v, want 3", gc)
	}
}

func TestE2EPaginationStartPage(t *testing.T) {
	cs := startTestServer(t)

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"university_name": "清华大学", "page_size": 1, "page": 5, "max_pages": 2,
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	note, _ := parsed["_sampling"].(string)
	if !strings.Contains(note, "第 5–6 页") {
		t.Fatalf("_sampling 应说明起始页为 5: %q", note)
	}
}

func TestE2EPaginationCostGuard(t *testing.T) {
	cs := startTestServer(t)
	before := len(gwPcodesSnapshot())

	// max_pages 超上限（schema maximum=10）应在协议层被拒 → 不产生任何网关调用（最省成本的护栏）
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "school_alumni_circle",
		Arguments: map[string]any{"university_name": "清华大学", "page_size": 100, "max_pages": 999},
	})
	if err == nil && res != nil && !res.IsError {
		t.Fatal("max_pages=999 应被拒绝（schema 上限 10）")
	}
	if len(gwPcodesSnapshot()) != before {
		t.Fatal("越界参数不应触达网关（零成本拒绝）")
	}

	// 上限内（10 页）应被接受，且页数受参数约束
	gwHits.Store(0)
	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"university_name": "清华大学", "page_size": 1, "max_pages": 10,
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	if got := gwHits.Load(); got != 10 {
		t.Fatalf("网关调用 = %d, want 10（max_pages=10 页各 1 次）", got)
	}
}

// ─────────────── 单元：统计摘要 TopN 解析 ───────────────

func TestTopNByName(t *testing.T) {
	arr := []any{
		map[string]any{"name": "北京", "entNum": float64(446)},
		map[string]any{"name": "安徽", "entNum": float64(194)},
		map[string]any{"name": "广东", "entNum": float64(342)},
		map[string]any{"name": "", "entNum": float64(999)}, // 无名字应跳过
		map[string]any{"entNum": float64(1)},               // 缺名字应跳过
		"not-a-map",                                        // 脏数据应跳过
	}
	top := topNByName(arr, 2)
	if len(top) != 2 {
		t.Fatalf("TopN 条数 = %d, want 2; %v", len(top), top)
	}
	if top[0]["name"] != "北京" || top[1]["name"] != "广东" {
		t.Fatalf("TopN 排序错误: %v", top)
	}
	if topNByName(nil, 5) == nil {
		t.Fatal("空输入应返回空切片而非 nil")
	}
}

// ─────────────── 端到端：新工具注册、单次网关调用、不泄露 pcode ───────────────

func TestE2ESchoolAlumniCircle(t *testing.T) {
	cs := startTestServer(t)
	offset := auditSize(t)
	gwHits.Store(0)
	before := gwPcodesSnapshot()

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"university_name": "中国科学技术大学",
	})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("调用失败: %s", out)
	}
	if strings.Contains(out, "E_006_0021") {
		t.Fatal("输出泄露了接口编码（脱敏失效）")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	if parsed["school"] != "中国科学技术大学" {
		t.Fatalf("school = %v", parsed["school"])
	}
	if _, ok := parsed["_note"]; !ok {
		t.Fatal("缺少 _note（数据覆盖说明）")
	}
	if _, ok := parsed["matched_enterprises"]; !ok {
		t.Fatal("缺少 matched_enterprises")
	}

	// 默认 1 次网关调用（校企筛选）
	if got := gwHits.Load(); got != 1 {
		t.Fatalf("网关调用 = %d, want 1", got)
	}
	after := gwPcodesSnapshot()
	if len(after) != len(before)+1 || after[len(after)-1] != "E_006_0021" {
		t.Fatalf("网关 pcode = %v, 末位应为 E_006_0021", after)
	}

	// 审计：工具层 gateway_calls=1
	lines, _ := readAuditLines(t, offset)
	var gc any
	for _, l := range lines {
		if l.Method == "tools/call" && l.Tool == "school_alumni_circle" && l.Extra != nil {
			gc = l.Extra["gateway_calls"]
		}
	}
	if v, ok := gc.(float64); !ok || int(v) != 1 {
		t.Fatalf("gateway_calls = %v, want 1", gc)
	}
}

func TestE2ESchoolAlumniCircleValidation(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)

	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{"university_name": "  "})
	if !strings.Contains(out, "university_name") {
		t.Fatalf("空校名应给出参数提示: %s", out)
	}
	if got := gwHits.Load(); got != 0 {
		t.Fatalf("本地校验失败不应触达网关, got %d", got)
	}
}

// 计费预估：默认 1 次；include_stats=true 时 2 次
func TestE2EEstimateBillingSchoolAlumni(t *testing.T) {
	cs := startTestServer(t)
	call := func(params map[string]any) map[string]any {
		out := callToolRaw(t, cs, "estimate_billing", map[string]any{
			"tool": "school_alumni_circle", "params": params,
		})
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("输出非 JSON: %v; %s", err, out)
		}
		return m
	}
	if got := call(map[string]any{}); int(got["estimated_calls"].(float64)) != 1 {
		t.Fatalf("默认 estimated_calls = %v, want 1", got["estimated_calls"])
	}
	if got := call(map[string]any{"include_stats": true}); int(got["estimated_calls"].(float64)) != 2 {
		t.Fatalf("include_stats estimated_calls = %v, want 2", got["estimated_calls"])
	}
	// 传全称 1 次；简称需 +1 次解析（成本可见，引导模型传全称）
	if got := call(map[string]any{"university_name": "中国科学技术大学"}); int(got["estimated_calls"].(float64)) != 1 {
		t.Fatalf("全称 estimated_calls = %v, want 1", got["estimated_calls"])
	}
	if got := call(map[string]any{"university_name": "中科大"}); int(got["estimated_calls"].(float64)) != 2 {
		t.Fatalf("简称 estimated_calls = %v, want 2（含解析）", got["estimated_calls"])
	}
	if got := call(map[string]any{"university_name": "中科大", "include_stats": true}); int(got["estimated_calls"].(float64)) != 3 {
		t.Fatalf("简称+stats estimated_calls = %v, want 3", got["estimated_calls"])
	}
	// 企业入口 3 次（工商 + 人员学历 + 筛选）
	if got := call(map[string]any{"company_name": "安徽智侒信信息技术有限公司"}); int(got["estimated_calls"].(float64)) != 3 {
		t.Fatalf("企业入口 estimated_calls = %v, want 3", got["estimated_calls"])
	}
}

// 同义词路由必须通过 MCP 检索层生效（盲测时"校友"检索为空）
func TestE2ESearchAlumniRoutesToSchoolAPIs(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "search_keeprisk_apis", map[string]any{"mode": "search", "query": "校友"})
	if !strings.Contains(out, "校企筛选V2") {
		t.Fatalf("检索「校友」未命中校企筛选V2: %s", out)
	}
	if !strings.Contains(out, "校企统计") {
		t.Fatalf("检索「校友」未命中校企统计: %s", out)
	}
}
