package keeprisk

import "testing"

// KEEPRISK_API_ROUTES 解析校验（政策 V2 部署在独立测试网关 + 专用 AppCode，2026-09-22）。
func TestParseAPIRoutes(t *testing.T) {
	// 空 = 未配置，合法
	m, err := parseAPIRoutes("", true)
	if err != nil || m != nil {
		t.Fatalf("空配置应返回 nil,nil: m=%v err=%v", m, err)
	}
	// 合法配置：pcode 大小写归一、URL 去尾斜杠
	m, err = parseAPIRoutes(`{"e_007_1441":{"base_url":"https://apitest.example.com:10001/","app_code":"apitest_appcode_001"}}`, false)
	if err != nil {
		t.Fatalf("合法配置报错: %v", err)
	}
	rt, ok := m["E_007_1441"]
	if !ok || rt.baseURL != "https://apitest.example.com:10001" || rt.appCode != "apitest_appcode_001" {
		t.Fatalf("路由解析异常: %+v", rt)
	}
	// 非法 JSON
	if _, err = parseAPIRoutes(`not-json`, true); err == nil {
		t.Fatal("非法 JSON 未报错")
	}
	// base_url 非 http(s)
	if _, err = parseAPIRoutes(`{"E_007_1441":{"base_url":"apitest:10001","app_code":"a1"}}`, false); err == nil {
		t.Fatal("base_url 缺协议前缀未报错")
	}
	// REQUIRE_TLS=true 时拒绝 http://
	if _, err = parseAPIRoutes(`{"E_007_1441":{"base_url":"http://apitest.example.com:10001","app_code":"a1"}}`, true); err == nil {
		t.Fatal("REQUIRE_TLS 下 http:// 未报错")
	}
	if _, err = parseAPIRoutes(`{"E_007_1441":{"base_url":"https://apitest.example.com:10001","app_code":"a1"}}`, true); err != nil {
		t.Fatalf("REQUIRE_TLS 下 https:// 不应报错: %v", err)
	}
	// app_code 含非法字符（AppCode 字符集：字母/数字/下划线/连字符）
	if _, err = parseAPIRoutes(`{"E_007_1441":{"base_url":"https://h:1","app_code":"bad code!"}}`, false); err == nil {
		t.Fatal("app_code 非法字符未报错")
	}
	// 形态合法但不存在（E_007_4141；含少一位的笔误 E_007_144——宽松形态检查
	// 接不住它，靠目录存在性校验拦截）/ 历史编码（E_011_0261，映射已删除、按未知
	// 编码本地拒绝，配在旧 pcode 上的路由永不命中）→ 均按配置错误拦截
	for _, bad := range []string{"E_007_4141", "E_007_144", "E_011_0261"} {
		raw := `{"` + bad + `":{"base_url":"https://h:1","app_code":"a1"}}`
		if _, err = parseAPIRoutes(raw, false); err == nil {
			t.Fatalf("不存在/历史 pcode %q 未报错（路由永不命中）", bad)
		}
	}
	// pcode 形态非法（非 pcode 形态的键，启动期快速拒绝）
	for _, bad := range []string{"E0071441", "政策列表", "X_007_1441"} {
		raw := `{"` + bad + `":{"base_url":"https://h:1","app_code":"a1"}}`
		if _, err = parseAPIRoutes(raw, false); err == nil {
			t.Fatalf("非法 pcode 键 %q 未报错（会静默回落主网关）", bad)
		}
	}
}

func TestRouteFor(t *testing.T) {
	c := &Client{base: "https://api.keeprisk.com:9080", routes: map[string]pcodeRoute{
		"E_007_1441": {baseURL: "https://apitest.example.com:10001", appCode: "apitest_appcode_001"},
	}}
	if rt, ok := c.routeFor("e_007_1441"); !ok || rt.baseURL != "https://apitest.example.com:10001" {
		t.Fatalf("routeFor 大小写归一失败: %+v ok=%v", rt, ok)
	}
	if _, ok := c.routeFor("E_007_0501"); ok {
		t.Fatal("未配置路由的 pcode 不应命中")
	}
}
