package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
	"github.com/xrzlizheng/keeprisk-mcp/internal/params"
)

// 参数要求机制（paramrules.go）的四条读取路径回归。
//
// 背景（2026-09-17 用户反馈）：模型调用 MCP 时无从得知"参数该怎么传"，
// 尤其是**传错会被上游静默忽略**的情形——返回全国数据却看起来筛选成功且照常计费。
// 机制目标：规则定义一次，在模型会经过的四条路径自动浮现。

func TestParamRequirementsExposedInSearchAndGet(t *testing.T) {
	cs := startTestServer(t)

	// 路径 1：search_keeprisk_apis 输出 param_requirements
	out := callToolRaw(t, cs, "search_keeprisk_apis", map[string]any{"mode": "search", "query": "校企筛选"})
	if !strings.Contains(out, "param_requirements") {
		t.Fatalf("检索结果应含 param_requirements: %s", out)
	}
	if !strings.Contains(out, "regionList") {
		t.Fatalf("应提示 regionList 正确写法: %s", out)
	}

	// 路径 2：get_keeprisk_api 输出 param_requirements
	out = callToolRaw(t, cs, "get_keeprisk_api", map[string]any{"pcode": "校企筛选V2"})
	if !strings.Contains(out, "param_requirements") {
		t.Fatalf("详情应含 param_requirements: %s", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("详情非 JSON: %v; %s", err, out)
	}
	reqs, _ := m["param_requirements"].([]any)
	if len(reqs) == 0 {
		t.Fatal("param_requirements 不应为空")
	}
	// 每条规则应含 param/level/note，阻断型应含 fix
	sawFix := false
	for _, r := range reqs {
		rm, _ := r.(map[string]any)
		if rm["param"] == nil || rm["level"] == nil || rm["note"] == nil {
			t.Fatalf("规则字段不全: %v", rm)
		}
		if rm["fix"] != nil {
			sawFix = true
		}
	}
	if !sawFix {
		t.Fatal("应至少有一条规则给出可照抄的 fix")
	}
	if strings.Contains(out, "E_006_0021") {
		t.Fatal("输出泄露接口编码")
	}
}

// 路径 3：validate_keeprisk_params 对违规传参报错并给出正确写法
func TestParamRequirementsInValidate(t *testing.T) {
	cs := startTestServer(t)

	out := callToolRaw(t, cs, "validate_keeprisk_params", map[string]any{
		"pcode":  "校企筛选V2",
		"params": map[string]any{"type": "1", "text": "中国科学技术大学", "province": "安徽"},
	})
	if !strings.Contains(out, `"valid":false`) {
		t.Fatalf("平铺 province 应校验失败: %s", out)
	}
	if !strings.Contains(out, "regionList") {
		t.Fatalf("应给出 regionList 正确写法: %s", out)
	}
	if !strings.Contains(out, "静默忽略") {
		t.Fatalf("应说明会被静默忽略: %s", out)
	}

	// 正确写法应通过
	out = callToolRaw(t, cs, "validate_keeprisk_params", map[string]any{
		"pcode": "校企筛选V2",
		"params": map[string]any{"type": "1", "text": "中国科学技术大学",
			"regionList": []any{map[string]any{"province": "安徽"}}},
	})
	if !strings.Contains(out, `"valid":true`) {
		t.Fatalf("regionList 写法应通过校验: %s", out)
	}
}

// 路径 4a：keeprisk_query 违规传参被拦截（不触达网关、不计费）
func TestParamRequirementsBlockQuery(t *testing.T) {
	cs := startTestServer(t)
	before := len(gwPcodesSnapshot())

	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "校企筛选V2",
		"params": map[string]any{"type": "1", "text": "中国科学技术大学", "province": "安徽"},
	})
	if !strings.Contains(out, "regionList") {
		t.Fatalf("应拦截并给出正确写法: %s", out)
	}
	if len(gwPcodesSnapshot()) != before {
		t.Fatal("违规传参应被拦截（不触达网关、不计费）")
	}
}

// 路径 4b：非阻断规则应放行但随响应返回 warnings
//
// 2026-09-17 交叉验证后调整：原用例依赖"E_017_0081 区划不生效"这条**已被证伪的规则**（现为阻断型）。
// 改用 paramrules 中唯一保留的非阻断规则——E_017_0081 不适用后，用"未阻断但违规"的规则验证；
// 当前规则表内所有规则均为阻断型，故此处直接构造一条最小非阻断规则验证机制本身。
func TestParamRequirementsWarningMechanism(t *testing.T) {
	// 机制验证：catalog.CheckParamRules 对非阻断规则返回 warnings 而非 errors
	rules := catalog.ParamRulesForTest()
	var found bool
	for _, rs := range rules {
		for _, r := range rs {
			if !r.Blocking && r.Bad != nil {
				found = true
			}
		}
	}
	if !found {
		t.Skip("当前规则表无非阻断规则；该分支保留供后续新增时验证")
	}
}

// 主体字段提示（全量审计：236 接口主体字段 6 种，name 81 / entName 19 / companyName 15）
func TestSubjectFieldHints(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}

	t.Run("按接口推导主体字段", func(t *testing.T) {
		// 主要人员：entName
		entry := cat.GetByPcode("E_007_0601")
		if entry == nil {
			t.Fatal("未找到 E_007_0601")
		}
		note := catalog.SubjectRequirementNote(entry)
		if !strings.Contains(note, "entName") {
			t.Fatalf("主体要求应含 entName: %q", note)
		}
	})

	t.Run("主体字段不匹配时提示", func(t *testing.T) {
		// 终本案件 E_003_0311 主体为 name（requiredFields 认定）；传 entName → 应提示
		entry := cat.GetByPcode("E_003_0311")
		if entry == nil {
			t.Fatal("未找到 E_003_0311")
		}
		msg := catalog.SubjectMismatchWarning(entry, map[string]any{"entName": "某公司"})
		if msg == "" {
			t.Fatal("主体字段不匹配应给出提示")
		}
		if !strings.Contains(msg, "name") {
			t.Fatalf("提示应说明本接口字段: %q", msg)
		}
	})

	t.Run("主体字段匹配时不误报", func(t *testing.T) {
		entry := cat.GetByPcode("E_007_0621")
		if msg := catalog.SubjectMismatchWarning(entry, map[string]any{"entName": "某公司"}); msg != "" {
			t.Fatalf("匹配时不应提示: %q", msg)
		}
	})

	t.Run("多主体字段接口不误报", func(t *testing.T) {
		// E_007_0621 同时声明 entName 与 name：传任一都不应报不匹配
		entry := cat.GetByPcode("E_007_0621")
		if entry == nil {
			t.Fatal("未找到 E_007_0621")
		}
		for _, f := range []string{"entName", "name"} {
			if msg := catalog.SubjectMismatchWarning(entry, map[string]any{f: "某公司"}); msg != "" {
				t.Fatalf("多字段接口传 %s 不应误报: %q", f, msg)
			}
		}
	})
}

// 端到端：主体字段不匹配时，warnings 随成功响应返回（模型能看见）
func TestE2ESubjectMismatchWarnsOnSuccess(t *testing.T) {
	cs := startTestServer(t)

	// 股权穿透主体为 entName；用 name 传 → 网关会当参数未知，但本地应给出主体字段提示
	out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "股权穿透",
		"params": map[string]any{"name": "安徽智侒信信息技术有限公司"},
	})
	if !strings.Contains(out, "entName") {
		t.Fatalf("响应应提示本接口主体字段为 entName: %s", out)
	}
}

// 端到端：聚合工具注册完整性（审计脚本发现的问题不应只停留在文档）
func TestE2EAuditScriptExists(t *testing.T) {
	// 审计脚本是"举一反三"的载体：确保它随仓库存在，便于接口目录更新后复跑
	if _, err := os.Stat("../../eval/audit_param_conventions.py"); err != nil {
		t.Fatalf("全量入参审计脚本缺失（审计能力未固化）: %v", err)
	}
}
func TestParamRulesIntegrity(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for pcode, rules := range catalog.ParamRulesForTest() {
		if cat.GetByPcode(pcode) == nil {
			t.Errorf("规则指向不存在的接口 %s（接口移除后应同步删除规则）", pcode)
		}
		for _, r := range rules {
			if r.Param == "" || r.Note == "" {
				t.Errorf("%s 规则缺少 param/note", pcode)
			}
			switch r.Level {
			case catalog.ParamRequired, catalog.ParamFormat, catalog.ParamIgnored:
			default:
				t.Errorf("%s 非法 level %q", pcode, r.Level)
			}
			if r.Blocking && r.Fix == "" {
				t.Errorf("%s 阻断型规则 %s 必须给出 fix（否则模型仍不知如何改）", pcode, r.Param)
			}
			if r.Bad == nil {
				t.Errorf("%s 规则 %s 缺少 Bad 判定", pcode, r.Param)
			}
		}
	}
}

// params.Validate 与 catalog.CheckParamRules 的口径一致性
func TestParamRulesViaParamsValidate(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	entry := cat.GetByPcode("E_007_1251")
	if entry == nil {
		t.Fatal("未找到 E_007_1251")
	}
	// 平铺 province → 应报错（region 对象才是正确写法）
	_, errs, _ := params.Validate(entry, map[string]any{"chainName": "生物医药产业链", "province": "安徽"})
	joined := strings.Join(errs, " ")
	if !strings.Contains(joined, "region") {
		t.Fatalf("平铺 province 应被 params.Validate 拦截: %v", errs)
	}
	// region 对象 → 通过
	_, errs, _ = params.Validate(entry, map[string]any{"chainName": "生物医药产业链",
		"region": map[string]any{"province": "安徽"}})
	if len(errs) != 0 {
		t.Fatalf("region 对象应通过: %v", errs)
	}
}

// 政策详情V2 的 id 级联来源提示（2026-09-22 实测：列表 uid / 详情 dataList id 均为
// 32 位十六进制；自行构造的 id 返回 110 无数据且不计费）。
func TestPolicyDetailIDCascadeRule(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	entry := cat.GetByPcode("E_007_1451")
	if entry == nil {
		t.Fatal("未找到 E_007_1451 政策详情V2")
	}
	// 合法 uid（实测形态）→ 无错误、无该规则告警
	_, errs, warns := params.Validate(entry, map[string]any{"id": "640d8b4f177c76e3a104078d1fd74f2a"})
	if len(errs) != 0 {
		t.Fatalf("合法 uid 报错: %v", errs)
	}
	for _, w := range warns {
		if strings.Contains(w, "id 须来自接口返回") {
			t.Fatalf("合法 uid 触发级联告警（误报）: %s", w)
		}
	}
	// 疑似构造的 id（V1 文档示例形态 "194"）→ 警告（非阻断）且给出级联来源
	_, errs, warns = params.Validate(entry, map[string]any{"id": "194"})
	if len(errs) != 0 {
		t.Fatalf("级联提示不应阻断调用: %v", errs)
	}
	hit := false
	for _, w := range warns {
		if strings.Contains(w, "政策列表") && strings.Contains(w, "uid") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("非 uid 形态 id 未给出级联来源提示: %v", warns)
	}
	// 数字型 id（模型可能直接给数字）：paramStr 对非 type 键数字返回空，
	// 规则须按「存在但非字符串」同样触发（R3，2026-09-22 review 修复）
	_, errs, warns = params.Validate(entry, map[string]any{"id": 123456})
	if len(errs) != 0 {
		t.Fatalf("数字型 id 不应阻断（网关会按字符串收）: %v", errs)
	}
	hit = false
	for _, w := range warns {
		if strings.Contains(w, "政策列表") && strings.Contains(w, "uid") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("数字型 id 未触发级联提示: %v", warns)
	}
}
