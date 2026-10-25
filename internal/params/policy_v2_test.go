package params

import (
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// 政策 V2 迁移（2026-09-22）：详情必填 id 拦截空参；列表 V2 参数免误报告警；
// V1 旧参数名 keyword 与文档残留 typeList 在 V2 上应被识别为未知并告警——
// 防止模型按 V1 记忆/文档参数表传参后静默失效。（容器参数 industryList/regionList
// 属全局 skipUnknown 豁免、由网关侧提示兜底，见断言处说明，不在头注宣称。）
func TestPolicyV2Validate(t *testing.T) {
	c, err := catalog.Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}

	detail := c.GetByPcode("E_007_1451")
	if detail == nil {
		t.Fatal("未找到 E_007_1451 政策详情V2")
	}
	// 空参必须拦在本地（V2 文档 id 必填），不能穿透到网关拿 101
	_, errs, _ := Validate(detail, map[string]any{})
	if !containsErr(errs, "缺少必填字段: id") {
		t.Fatalf("详情空参未拦截 id 必填: errs=%v", errs)
	}
	_, errs, warns := Validate(detail, map[string]any{"id": "0c8f5b4e362dac7e1c98cca85f28aad3"})
	if len(errs) > 0 {
		t.Fatalf("详情合法入参报错: errs=%v", errs)
	}
	for _, w := range warns {
		if strings.Contains(w, "未知参数 id") {
			t.Fatalf("id 被误报未知参数: %s", w)
		}
	}

	list := c.GetByPcode("E_007_1441")
	if list == nil {
		t.Fatal("未找到 E_007_1441 政策列表V2")
	}
	// V2 全新参数（含嵌套对象/数组）不应触发未知参数告警
	_, errs, warns = Validate(list, map[string]any{
		"searchKey":   map[string]any{"type": "2", "text": "人工智能"},
		"policyType":  []any{"申报通知"},
		"deptNameStd": []any{"工业科技与信息化类"},
		"subsidyType": []any{"资金奖补"},
		"region":      []any{map[string]any{"pr": "安徽"}},
		"publishTime": []any{map[string]any{"from": "2025-01-01", "to": "2026-12-31"}},
		"pageNo":      "1",
		"pageSize":    "10",
	})
	if len(errs) > 0 {
		t.Fatalf("列表 V2 合法入参报错: errs=%v", errs)
	}
	for _, w := range warns {
		if strings.Contains(w, "未知参数") {
			t.Fatalf("V2 参数被误报未知: %s", w)
		}
	}
	// typeList（文档参数表列名，实测被网关静默忽略）应作为未知参数告警，
	// 阻止模型照文档照抄拿到"看似成功、实际未过滤"的结果
	_, _, warns = Validate(list, map[string]any{"typeList": []any{"申报通知"}})
	foundTypeList := false
	for _, w := range warns {
		if strings.Contains(w, "未知参数 typeList") {
			foundTypeList = true
		}
	}
	if !foundTypeList {
		t.Fatalf("typeList（实测静默忽略参数）未被识别为未知参数: %v", warns)
	}
	// V1 主体参数 keyword 在 V2 上是未知参数（应告警而非静默接受）。
	// 注：V1 的容器参数 industryList/regionList 属全局 skipUnknown 复合参数集合
	// （高级筛选聚合工具构建，告警豁免是既有设计），由网关侧「未知参数」提示兜底，
	// 此处不断言。
	_, _, warns = Validate(list, map[string]any{
		"keyword": map[string]any{"text": "人工智能"},
	})
	foundKeyword := false
	for _, w := range warns {
		if strings.Contains(w, "未知参数 keyword") {
			foundKeyword = true
		}
	}
	if !foundKeyword {
		t.Fatalf("V1 旧参数 keyword 未被识别为未知参数: %v", warns)
	}
}

func containsErr(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}
