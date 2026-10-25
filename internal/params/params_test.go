package params

import (
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

func TestValidate_MirrorsNameFromEntNameWhenCatalogRequiresName(t *testing.T) {
	entry := &catalog.APIEntry{
		Pcode: "E_007_0401",
		BusinessParams: []catalog.BusinessParam{
			{Name: "entName"},
			{Name: "layer"},
			{Name: "name"},
		},
		RequiredOneOf: []any{[]any{"name"}},
		ExampleRequest: map[string]any{
			"entName": "北大方正集团有限公司",
			"layer":   2,
		},
	}
	norm, errs, _ := Validate(entry, map[string]any{"entName": "华为技术有限公司", "layer": 2})
	if len(errs) > 0 {
		t.Fatalf("errs=%v, want none", errs)
	}
	if norm["name"] != "华为技术有限公司" {
		t.Fatalf("name=%v, want mirrored entName", norm["name"])
	}
	if norm["entName"] != "华为技术有限公司" {
		t.Fatalf("entName=%v", norm["entName"])
	}
}

func TestValidate_MirrorsNameFromCompanyName(t *testing.T) {
	entry := &catalog.APIEntry{
		Pcode: "E_010_0211",
		BusinessParams: []catalog.BusinessParam{
			{Name: "companyName"},
			{Name: "number"},
			{Name: "name"},
		},
		RequiredOneOf: []any{[]any{"name"}},
	}
	norm, errs, _ := Validate(entry, map[string]any{
		"companyName": "贵州茅台酒股份有限公司",
		"number":      "0",
	})
	if len(errs) > 0 {
		t.Fatalf("errs=%v", errs)
	}
	if norm["name"] != "贵州茅台酒股份有限公司" {
		t.Fatalf("name=%v", norm["name"])
	}
}

func TestValidate_AcceptsEntNameForNameOneOfWithoutMirrorWhenNameNotDeclared(t *testing.T) {
	// 仅 required_one_of 误标 name、business_params 无 name 时，等价接受 entName
	entry := &catalog.APIEntry{
		Pcode: "E_TEST_NAME",
		BusinessParams: []catalog.BusinessParam{
			{Name: "entName"},
		},
		RequiredOneOf: []any{[]any{"name"}},
	}
	_, errs, _ := Validate(entry, map[string]any{"entName": "测试公司"})
	if len(errs) > 0 {
		t.Fatalf("errs=%v, entName should satisfy mislabeled name one_of", errs)
	}
}

func TestValidate_StillRequiresTrueMissingFields(t *testing.T) {
	entry := &catalog.APIEntry{
		Pcode:         "E_007_0401",
		BusinessParams: []catalog.BusinessParam{{Name: "entName"}, {Name: "name"}},
		RequiredOneOf: []any{[]any{"name"}},
	}
	_, errs, _ := Validate(entry, map[string]any{"layer": 2})
	if len(errs) == 0 {
		t.Fatal("expected validation error when no subject field")
	}
}

// ──────────── 目录 required_one_of 误标 name 的纠正（2026-09-18）────────────
//
// 内嵌目录有 61 条 required_one_of=[["name"]] 但 business_params 无 name 的条目
// （上市公司族 E_010_* 为主）。若不纠正，本地校验报错会指引调用方去传 name，
// 而网关回"未知参数 name"——把调用方指向一个必然失败的字段。
func TestNormalizeNameOnlyGroups(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// E_010_0181 上市公司分红转增：req=[["name"]]，实际主体字段 companyName
	entry := cat.GetByPcode("E_010_0181")
	if entry == nil {
		t.Fatal("E_010_0181 不在目录中")
	}
	groups := requiredOneOfGroups(entry)
	if len(groups) != 1 || len(groups[0]) != 1 {
		t.Fatalf("group 形态异常: %v", groups)
	}
	if groups[0][0] == "name" {
		t.Errorf("E_010_0181 的 required_one_of 仍指向 name（应改写为 companyName）: %v", groups)
	}
	if groups[0][0] != "companyName" {
		t.Errorf("E_010_0181 应改写为 companyName，实际 %v", groups)
	}

	// 链路一致性：用 companyName 调用应通过校验；用 name 调用应被拦（因为不是本接口字段）
	_, errs, _ := Validate(entry, map[string]any{"companyName": "飞友科技有限公司"})
	if len(errs) > 0 {
		t.Errorf("传 companyName 应通过校验，实际 errors=%v", errs)
	}

	// 产业链节点统计：req=[["name"]]，实际主体字段 chainName
	if e := cat.GetByPcode("E_007_1251"); e != nil {
		g := requiredOneOfGroups(e)
		if len(g) > 0 && len(g[0]) > 0 && g[0][0] != "chainName" {
			t.Errorf("E_007_1251 应改写为 chainName，实际 %v", g)
		}
	}
}

// E_007_1421 上市公司主要人员：required=[entName,companyId] 是**正确**的
// （文档业务参数原文「entName…与companyId 必填其一」；实测网关 entName 通过校验到达 107
// 未订阅，而 companyName 不被接受）。此前目录元数据已标注"示例与 required 不一致，待上游确认"，
// 实测定案：**文档示例的 companyName 才是错的一方**。
// 该用例锁定：整组改写机制不得误伤此条（运行时 business_params 已含 entName/companyId）。
func TestE0071421KeepsEntNameGroup(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	entry := cat.GetByPcode("E_007_1421")
	if entry == nil {
		t.Fatal("E_007_1421 不在目录中")
	}
	groups := requiredOneOfGroups(entry)
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("E_007_1421 required 组形态异常: %v", groups)
	}
	if groups[0][0] != "entName" || groups[0][1] != "companyId" {
		t.Errorf("E_007_1421 应保留 [entName companyId]，实际 %v", groups)
	}
	// 按 required 传 entName 应通过本地校验
	if _, errs, _ := Validate(entry, map[string]any{"entName": "贵州茅台酒股份有限公司"}); len(errs) > 0 {
		t.Errorf("传 entName 应通过校验，实际 errors=%v", errs)
	}
	// 传 companyName（文档示例，网关不接受）应被本地拦下——这是保护行为
	if _, errs2, _ := Validate(entry, map[string]any{"companyName": "贵州茅台酒股份有限公司"}); len(errs2) == 0 {
		t.Error("传 companyName 应被本地校验拦截（网关不接受该字段）")
	}
}

// 整组字段确实都未声明时的改写行为（构造条目验证机制本身）。
func TestNormalizeUndeclaredGroupRewrites(t *testing.T) {
	// 注意经 JSON 反序列化的 RequiredOneOf 是 []any（生产形态）；
	// 若直接用 [][]string 字面量会绕过 requiredOneOfGroups 的类型分支
	entry := &catalog.APIEntry{
		Pcode:          "X_TEST_1",
		BusinessParams: []catalog.BusinessParam{{Name: "companyName"}, {Name: "number"}},
		InputSchema: map[string]any{
			"required_one_of": []any{[]any{"entName", "companyId"}},
		},
	}
	groups := requiredOneOfGroups(entry)
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0] != "companyName" {
		t.Errorf("整组未声明应改写为 [companyName]，实际 %v", groups)
	}
}

// E_010_1201 定量评级的主体字段确实是 name（文档/目录/聚合器三方一致），
// 不得被批量修正误伤。
func TestE0101201KeepsName(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	entry := cat.GetByPcode("E_010_1201")
	if entry == nil {
		t.Fatal("E_010_1201 不在目录中")
	}
	_, errs, _ := Validate(entry, map[string]any{"name": "贵州茅台酒股份有限公司"})
	if len(errs) > 0 {
		t.Errorf("传 name 应通过校验，实际 errors=%v", errs)
	}
	// 注：传 companyName 也会通过——镜像机制会把 companyName 回填进已声明的 name，
	// 网关对两个键都接受（E_010_1201 文档主体为 name，mirror 保证兼容）。
	// 关键断言是上一条：传 name 必须通过（不得被批量修正误伤）。
	if _, errs2, _ := Validate(entry, map[string]any{"companyName": "贵州茅台酒股份有限公司"}); len(errs2) != 0 {
		t.Errorf("传 companyName 经镜像也应通过，实际 errors=%v", errs2)
	}
}
