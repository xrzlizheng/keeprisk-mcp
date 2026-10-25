package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// 政策接口 V2 迁移回归（2026-09-22）：
// E_011_0261 政策列表 / E_011_0271 政策详情 已弃用，全量切换到
// E_007_1441 政策列表V2 / E_007_1451 政策详情V2（V2 文档：响应 pcode 与文件名一致，
// 文档参数表中的「固定值:E_011_02x1」为模板残留，以响应示例为准）。
// 旧 pcode 语义（2026-09-22 定标）：**不自动升级**——历史编码不可解析、不可见，
// 调用侧本地拒绝并引导当前接口；meta.deprecated_pcodes 仅作"已下线登记表"用于精确提示。
func TestPolicyV2Migration(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}

	// 1) 旧 pcode 与未知编码**同等待遇**（2026-09-22 定标：映射已删除）：
	//    不可解析、本地拒绝、文案统一为"未知接口编码"、不回显编码、不给替代提示
	for _, old := range []string{"E_011_0261", "E_011_0271"} {
		if e := c.GetByPcode(old); e != nil {
			t.Fatalf("GetByPcode(%s) 应返回 nil（历史编码不可解析），got %s", old, e.Pcode)
		}
		msg := SuggestPcodeFix(c, old)
		if !strings.Contains(msg, "未知接口编码") {
			t.Fatalf("SuggestPcodeFix(%s) 应给统一未知文案: %q", old, msg)
		}
		if strings.Contains(msg, "已下线") || strings.Contains(msg, old) {
			t.Fatalf("拒绝文案不应给替代提示或回显编码: %q", msg)
		}
	}
	// 映射数据已删除（防回潮）：导出 meta 不得再声明弃用映射。
	// 注：条目内 supersedes_pcodes 属上游历史元数据（出口脱敏会移除该键、模型不可见），
	// 不作为"回潮"判据。
	b, err := c.EffectiveJSON()
	if err != nil {
		t.Fatalf("EffectiveJSON: %v", err)
	}
	var dumped struct {
		Meta struct {
			DeprecatedCount int               `json:"deprecated_count"`
			Deprecated      map[string]string `json:"deprecated_pcodes"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(b, &dumped); err != nil {
		t.Fatalf("解析导出: %v", err)
	}
	if dumped.Meta.DeprecatedCount != 0 || len(dumped.Meta.Deprecated) != 0 {
		t.Fatalf("有效目录导出仍声明弃用映射: count=%d map=%v", dumped.Meta.DeprecatedCount, dumped.Meta.Deprecated)
	}

	// 2) 旧 pcode 不再以活跃条目存在（目录 apis 已移除）
	for _, p := range []string{"E_011_0261", "E_011_0271"} {
		for _, a := range c.LatestAPIs() {
			if a.Pcode == p {
				t.Fatalf("旧政策接口 %s 仍为活跃条目（应仅存于 deprecated_pcodes）", p)
			}
		}
	}

	// 3) V2 列表条目：标题保持「政策列表」（title 路由不受迁移影响），V2 参数齐备
	list := c.GetByPcode("E_007_1441")
	if list == nil {
		t.Fatal("未找到 E_007_1441 政策列表V2")
	}
	if list.Title != "政策列表" || list.Category != "政策" {
		t.Fatalf("V2 列表条目标题/分类异常: title=%q category=%q", list.Title, list.Category)
	}
	have := map[string]bool{}
	for _, p := range list.BusinessParams {
		have[p.Name] = true
	}
	// searchKey/industryCategory/publishTime/region 等为 V2 全新命名（V1 的
	// keyword/industryList/regionList 已废弃）；政策类型筛选项为 policyType——
	// 2026-09-22 apitest 网关实测裁决：文档参数表的 typeList 被网关接受但**静默忽略**
	// （返回混合类型 total=1615），policyType 才真正过滤（total=169 全为申报通知），
	// 故目录只保留 policyType（见 eval/merge_param_meta.py MANUAL_REMOVE）。
	for _, want := range []string{
		"searchKey", "industryCategory", "publishTime", "region",
		"deptNameStd", "subsidyType", "policyType", "deptName", "deptLevel",
		"pageNo", "pageSize",
	} {
		if !have[want] {
			t.Fatalf("政策列表V2 缺少参数 %s（business_params: %v）", want, keysOf(have))
		}
	}
	for _, stale := range []string{"keyword", "eq", "no_eq", "involveCompany", "themeList", "labelList", "industryList", "regionList", "deptLevelList", "recommendList", "typeList"} {
		if have[stale] {
			t.Fatalf("政策列表V2 仍带无效参数 %s", stale)
		}
	}
	if list.ExampleRequest == nil {
		t.Fatal("政策列表V2 缺 example_request（PDF 示例提取失败时应由目录条目自带）")
	}
	if _, ok := list.ExampleRequest["policyType"]; !ok {
		t.Fatalf("政策列表V2 example 应使用 policyType（typeList 实测静默忽略）: %v", list.ExampleRequest)
	}

	// 4) V2 详情条目：id 必填语义由 requiredFields 承载，示例含 id
	detail := c.GetByPcode("E_007_1451")
	if detail == nil {
		t.Fatal("未找到 E_007_1451 政策详情V2")
	}
	if detail.Title != "政策详情" {
		t.Fatalf("V2 详情条目标题异常: %q", detail.Title)
	}
	if detail.ExampleRequest == nil || detail.ExampleRequest["id"] == nil {
		t.Fatalf("政策详情V2 example 应含 id: %v", detail.ExampleRequest)
	}

	// 5) uid↔id 等价关系必须写在元数据里（2026-09-22 实测：列表返回 uid、
	//    详情收 id，是同一个 32 位主键；列表项无 id 字段，模型最容易在此处困惑）
	hint := ""
	for _, p := range detail.BusinessParams {
		if p.Name == "id" {
			hint = p.Hint
		}
	}
	if !strings.Contains(hint, "uid") {
		t.Fatalf("详情 id 的 hint 未说明与列表 uid 的等价关系: %q", hint)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
