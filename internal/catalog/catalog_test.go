package catalog

import (
	"testing"
)

// 回归：LoadJSON 必须在加载期剔除 input_schema 中空字符串的 "description"，
// 保证 get_keeprisk_api 等所有消费点不向 LLM 透传空描述噪音。
func TestLoadJSONStripsEmptyDescriptions(t *testing.T) {
	data := []byte(`{"meta":{"deprecated_pcodes":{}},"apis":[
		{"pcode":"E_007_1331","title":"企业筛选V4","is_latest":true,
		 "input_schema":{"type":"object","properties":{
			"region":{"type":"string","description":""},
			"pageNo":{"type":"string","description":"当前页（默认 1）"},
			"nested":{"type":"object","properties":{"x":{"type":"string","description":"  "}}}
		 }}}
	]}`)
	c, err := LoadJSON(data)
	if err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	item := c.GetByPcode("E_007_1331")
	if item == nil {
		t.Fatalf("E_007_1331 not loaded")
	}
	props := item.InputSchema["properties"].(map[string]any)
	// 空描述只剔除 description 键本身，保留属性（参数存在性信息必须保留）
	region, ok := props["region"].(map[string]any)
	if !ok {
		t.Fatalf("region 属性应保留（仅剔除空 description）；props=%v", props)
	}
	if _, hasDesc := region["description"]; hasDesc {
		t.Fatalf("region 的空 description 键应被剔除；region=%v", region)
	}
	if _, exists := props["pageNo"]; !exists {
		t.Fatalf("非空 description 属性 pageNo 不应被剔除")
	}
	nested := props["nested"].(map[string]any)["properties"].(map[string]any)
	x, ok := nested["x"].(map[string]any)
	if !ok {
		t.Fatalf("嵌套属性 x 应保留")
	}
	if _, hasDesc := x["description"]; hasDesc {
		t.Fatalf("嵌套的空白 description 键应被剔除；x=%v", x)
	}
	if item.InputSchema["type"] != "object" {
		t.Fatalf("其它键不应受影响；schema=%v", item.InputSchema)
	}
}

// 回归：业务参数的空 hint 不受清理逻辑影响（仅处理 description 键）。
//
// 注意：LoadJSON 会合并 param_meta（原始文档参数），故此处用**目录中不存在的 pcode**
// 隔离验证——若用真实 pcode，断言会被文档补充的参数影响（2026-09-17 踩过）。
func TestLoadJSONKeepsBusinessParams(t *testing.T) {
	data := []byte(`{"apis":[{"pcode":"E_999_9999","title":"测试用接口","is_latest":true,
		"business_params":[{"name":"entName","hint":""}]}]}`)
	c, err := LoadJSON(data)
	if err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	item := c.GetByPcode("E_999_9999")
	if item == nil || len(item.BusinessParams) != 1 || item.BusinessParams[0].Name != "entName" {
		t.Fatalf("business_params 被误改：%+v", item)
	}
}

// 回归：原始文档参数被正确合并（补齐目录未列出的参数，如 E_007_0501 的 companyId）
func TestLoadJSONMergesDocParams(t *testing.T) {
	data := []byte(`{"apis":[{"pcode":"E_007_0501","title":"工商基本信息","is_latest":true,
		"business_params":[{"name":"entName","hint":""}]}]}`)
	c, err := LoadJSON(data)
	if err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	item := c.GetByPcode("E_007_0501")
	if item == nil {
		t.Fatal("未找到条目")
	}
	names := map[string]bool{}
	for _, p := range item.BusinessParams {
		names[p.Name] = true
	}
	if !names["entName"] {
		t.Fatal("目录原有参数 entName 应保留")
	}
	if !names["companyId"] {
		t.Fatalf("文档参数 companyId 应被补充（目录未列出）；实际=%v", names)
	}
	if !item.ExampleFromDoc {
		t.Fatal("示例应标记为源自原始文档")
	}
	if len(item.DocParams) == 0 {
		t.Fatal("DocParams 应含文档参数元数据")
	}
}

// 回归：is_latest 缺省与 SupersededBy 的既有逻辑不因 schema 清理改变。
func TestLoadJSONIsLatestDefaults(t *testing.T) {
	data := []byte(`{"apis":[
		{"pcode":"E_001_0001","title":"无标记"},
		{"pcode":"E_001_0002","title":"被替代","superseded_by":"E_001_0003"}
	]}`)
	c, err := LoadJSON(data)
	if err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	// GetByPcode 只返回 is_latest 条目；内部字段直接遍历 apis 断言
	for i := range c.apis {
		switch c.apis[i].Pcode {
		case "E_001_0001":
			if !c.apis[i].IsLatest {
				t.Fatalf("缺省 is_latest 应为 true")
			}
		case "E_001_0002":
			if c.apis[i].IsLatest {
				t.Fatalf("有 superseded_by 时 is_latest 应为 false")
			}
		}
	}
	if got := c.GetByPcode("E_001_0001"); got == nil {
		t.Fatalf("latest 条目应可经 GetByPcode 获取")
	}
	if got := c.GetByPcode("E_001_0002"); got != nil {
		t.Fatalf("被替代条目不应经 GetByPcode 返回；got=%+v", got)
	}
}
