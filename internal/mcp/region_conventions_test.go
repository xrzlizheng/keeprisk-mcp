package mcp

import (
	"context"
	"sync"
	"encoding/json"
	"strings"
	"testing"
)

// 区划参数约定回归测试（2026-09-17 逐项线上实测）。
//
// 背景：目录元数据**不声明真实的区划容器参数**（46 个接口只声明扁平 province/city/county），
// 而各接口实际约定互不相同——扁平传参会被上游**静默忽略且照常计费**，
// 是最危险的一类问题（返回的是全国数据却看起来像筛选成功）。
//
// 实测结论（务必保持同步，改动前先线上复验）：
//
//	E_006_0021 校企筛选V2     → regionList: [{province,city,county}]（数组）
//	E_007_1131 产业链企业筛选  → regionList: [{province}]（数组）
//	E_007_1251 产业链节点统计  → region: {province,city}（对象！扁平无效）
//	E_007_1351 产业链地区统计  → 扁平 province（regionList 无效）
//	E_007_1171 产业链财务数据  → 扁平 province（regionList 报 101）
//	E_017_0081 产业链数据分析  → 区划不生效（province 被忽略）

func TestBuildRegionListForSchoolFilter(t *testing.T) {
	// E_006_0021 要求 regionList 数组；province 是必需锚点
	rl, errOut := buildRegionList("安徽", "合肥市", "蜀山区")
	if errOut != "" {
		t.Fatalf("应构造成功: %s", errOut)
	}
	if len(rl) != 1 || rl[0]["province"] != "安徽" || rl[0]["city"] != "合肥市" || rl[0]["county"] != "蜀山区" {
		t.Fatalf("regionList = %v", rl)
	}

	// 全空 → 不收窄（全国）
	if rl, errOut := buildRegionList("", "", ""); rl != nil || errOut != "" {
		t.Fatalf("空区划应返回 nil/无错误, got %v %q", rl, errOut)
	}

	// 仅市/区、缺 province → 本地报错（上游会静默忽略，比报错更危险）
	for _, tc := range [][3]string{{"", "合肥市", ""}, {"", "", "蜀山区"}} {
		_, errOut := buildRegionList(tc[0], tc[1], tc[2])
		if errOut == "" {
			t.Fatalf("缺 province 应本地报错（避免静默忽略+计费）：%v", tc)
		}
		if !strings.Contains(errOut, "province") {
			t.Fatalf("错误应指明 province 为锚点: %s", errOut)
		}
	}
}

func TestRegionListPropagatesToGateway(t *testing.T) {
	svc := &Services{}
	var got map[string]any
	svc.QueryFn = func(_ context.Context, pcode string, params map[string]any) string {
		if pcode == pcodeSchoolFilter {
			got = params
		}
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}, "total": 0}})
	}
	raw := svc.SchoolAlumniCircle(context.Background(), SchoolAlumniOpts{
		UniversityName: "中国科学技术大学", Province: "安徽", City: "合肥市",
	})
	if strings.Contains(raw, `"error"`) {
		t.Fatalf("调用失败: %s", raw)
	}
	rl, ok := got["regionList"].([]map[string]string)
	if !ok || len(rl) != 1 {
		t.Fatalf("网关收到的 regionList 缺失或类型错误: %#v", got["regionList"])
	}
	if rl[0]["province"] != "安徽" || rl[0]["city"] != "合肥市" {
		t.Fatalf("regionList 内容错误: %v", rl[0])
	}
	// 不得同时传扁平 province（避免歧义与"看起来生效"的假象）
	if _, flat := got["province"]; flat {
		t.Fatal("不应同时传扁平 province")
	}
}

func TestSchoolAlumniRejectsRegionWithoutProvince(t *testing.T) {
	svc := &Services{}
	called := false
	svc.QueryFn = func(_ context.Context, _ string, _ map[string]any) string {
		called = true
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}}})
	}
	raw := svc.SchoolAlumniCircle(context.Background(), SchoolAlumniOpts{
		UniversityName: "中国科学技术大学", City: "合肥市", // 缺 province
	})
	if !strings.Contains(raw, "province") {
		t.Fatalf("缺 province 应本地报错: %s", raw)
	}
	if called {
		t.Fatal("本地校验失败不应触达网关（不计费）")
	}
}

// 产业链画像：E_007_1251 必须用 region 对象，E_007_1351/E_007_1171 用扁平 province
func TestIndustryChainProfileRegionConventions(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	seen := map[string]map[string]any{}
	svc.QueryFn = func(_ context.Context, pcode string, params map[string]any) string {
		// 聚合工具并发调用本闭包，共享 map 必须加锁（-race 检测到竞争）
		cp := map[string]any{}
		for k, v := range params {
			cp[k] = v
		}
		mu.Lock()
		seen[pcode] = cp
		mu.Unlock()
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{}})
	}
	_ = svc.IndustryChainProfile(context.Background(), "人工智能产业链", "安徽", "合肥市")

	// E_007_1251：region 对象，且不得出现扁平 province
	params, ok := seen["E_007_1251"]
	if !ok {
		t.Fatal("未调用 E_007_1251")
	}
	region, ok := params["region"].(map[string]any)
	if !ok {
		t.Fatalf("E_007_1251 应传 region 对象, got %#v", params["region"])
	}
	if region["province"] != "安徽" {
		t.Fatalf("region.province = %v, want 安徽", region["province"])
	}
	if _, flat := params["province"]; flat {
		t.Fatal("E_007_1251 不得传扁平 province（实测被静默忽略且计费）")
	}

	// E_007_1351 / E_007_1171：扁平 province（regionList 无效）
	for _, pcode := range []string{"E_007_1351", "E_007_1171"} {
		params, ok := seen[pcode]
		if !ok {
			t.Fatalf("未调用 %s", pcode)
		}
		if params["province"] != "安徽" {
			t.Fatalf("%s 应用扁平 province, got %#v", pcode, params["province"])
		}
		if _, bad := params["regionList"]; bad {
			t.Fatalf("%s 不应传 regionList（实测无效）", pcode)
		}
	}
}

// 端到端：school_alumni_circle 区划参数经 MCP 协议生效且不泄露 pcode
func TestE2ESchoolAlumniWithRegion(t *testing.T) {
	cs := startTestServer(t)
	out := callToolRaw(t, cs, "school_alumni_circle", map[string]any{
		"university_name": "中国科学技术大学", "province": "安徽", "city": "合肥市",
	})
	if strings.Contains(out, `"error"`) {
		t.Fatalf("带区划调用应成功: %s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v; %s", err, out)
	}
	if strings.Contains(out, "E_006_0021") {
		t.Fatal("输出泄露接口编码")
	}
}
