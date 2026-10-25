package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// 业务输出不得包含接口编码：pcode_used/pcode/_redirect 键应被移除，
// warnings 等文本中的编码字样应被替换。
func TestSanitizeOutputRemovesKeys(t *testing.T) {
	out := mustJSON(map[string]any{
		"code":       100,
		"pcode_used": "E_007_0501",
		"_cache_hit": true,
		"warnings":   []any{"已弃用 E_007_0921，自动改用最新版 E_007_1331"},
		"data": map[string]any{
			"entName": "小米科技有限责任公司",
			"pcode":   "E_007_0501",
		},
	})
	cleaned := sanitizeOutput(out)
	parsed := mustParseJSON(t, cleaned)
	if _, ok := parsed["pcode_used"]; ok {
		t.Fatalf("顶层 pcode_used 应被移除; parsed=%v", parsed)
	}
	if _, ok := parsed["_cache_hit"]; ok == false {
		t.Fatalf("_cache_hit 应保留（缓存命中对成本可观测有价值）; parsed=%v", parsed)
	}
	data := parsed["data"].(map[string]any)
	if _, ok := data["pcode"]; ok {
		t.Fatalf("嵌套 pcode 键应被移除; data=%v", data)
	}
	ws := parsed["warnings"].([]any)[0].(string)
	if strings.Contains(ws, "E_007_0921") || strings.Contains(ws, "E_007_1331") {
		t.Fatalf("warnings 文本中的接口编码应被替换; ws=%q", ws)
	}
	if !strings.Contains(ws, "〔内部接口〕") {
		t.Fatalf("替换占位符应出现; ws=%q", ws)
	}
	// 业务字段不受影响
	if data["entName"] != "小米科技有限责任公司" {
		t.Fatalf("业务字段被误改; data=%v", data)
	}
}

// 目录/元数据类工具不再豁免：所有工具输出统一清洗（编码不对客暴露，
// 已持码调用方仍可通过入参直调）。
func TestSanitizeOutputAppliesToDirectoryTools(t *testing.T) {
	out := mustJSON(map[string]any{"pcode": "E_007_0501", "pcode_used": "E_007_0501", "valid": true})
	for _, tool := range []string{"keeprisk_query", "get_keeprisk_api", "search_keeprisk_apis", "validate_keeprisk_params"} {
		_ = tool // sanitizeOutput 不再区分工具
		cleaned := sanitizeOutput(out)
		parsed := mustParseJSON(t, cleaned)
		if _, ok := parsed["pcode"]; ok {
			t.Fatalf("目录类工具输出 pcode 也应被移除; parsed=%v", parsed)
		}
		if _, ok := parsed["pcode_used"]; ok {
			t.Fatalf("目录类工具输出 pcode_used 也应被移除; parsed=%v", parsed)
		}
		if parsed["valid"] != true {
			t.Fatalf("业务字段不应受影响; parsed=%v", parsed)
		}
	}
}

// 非 JSON 输出（极少）：字符串中的编码字样仍应被替换。
func TestSanitizeOutputSubstitutesNonJSON(t *testing.T) {
	s := "调用 E_003_0702 失败，请检查参数"
	got := sanitizeOutput(s)
	if strings.Contains(got, "E_003_0702") {
		t.Fatalf("非 JSON 输出中的编码应被替换; got=%q", got)
	}
	if !strings.Contains(got, "〔内部接口〕") {
		t.Fatalf("占位符应出现; got=%q", got)
	}
}

// 显式 KEEPRISK_HIDE_PCODE=false 关闭清洗（仅调试用，启动日志会告警）。
func TestSanitizeOutputDisabledByEnv(t *testing.T) {
	t.Setenv("KEEPRISK_HIDE_PCODE", "false")
	out := mustJSON(map[string]any{"pcode_used": "E_007_0501"})
	if got := sanitizeOutput(out); got != out {
		t.Fatalf("HIDE_PCODE=false 时不应清洗; got=%q", got)
	}
}

// 聚合工具输出中嵌套在 sections 里的 pcode 键也应被递归移除。
func TestSanitizeOutputRemovesSectionPcodes(t *testing.T) {
	out := mustJSON(map[string]any{
		"entity": "贵州茅台酒股份有限公司",
		"sections": []any{
			map[string]any{"key": "equity_pledge_detail", "pcode": "E_010_0411", "title": "股权质押明细"},
		},
	})
	cleaned := sanitizeOutput(out)
	parsed := mustParseJSON(t, cleaned)
	sec := parsed["sections"].([]any)[0].(map[string]any)
	if _, ok := sec["pcode"]; ok {
		t.Fatalf("sections[].pcode 应被移除; sec=%v", sec)
	}
	if sec["key"] != "equity_pledge_detail" {
		t.Fatalf("业务键 key 不应受影响; sec=%v", sec)
	}
	// 序列化应为合法 JSON
	var m map[string]any
	if err := json.Unmarshal([]byte(cleaned), &m); err != nil {
		t.Fatalf("清洗后输出非法 JSON: %v", err)
	}
}

// 敏感基础设施键（base_url/apikey/app_code/token/markdown_path/supersedes_pcodes）
// 应被移除，且大小写不敏感。
func TestSanitizeOutputRemovesSensitiveKeys(t *testing.T) {
	out := mustJSON(map[string]any{
		"base_url":          "https://api.keeprisk.com:9080",
		"baseUrl":           "https://api.keeprisk.com:9080",
		"apikey":            "ak-secret",
		"api_key":           "ak-secret",
		"app_code":          "AppCodeAlice",
		"AppCode":           "AppCodeAlice",
		"token":             "tok-secret",
		"authorization":     "Bearer tok-secret",
		"markdown_path":     "markdown/xxx/[E_007_0601]main_person.md",
		"supersedes_pcodes": []any{"E_007_0921"},
		"pcode":             "E_007_0501",
		"Pcode":             "E_007_0501",
		"data":              map[string]any{"entName": "小米科技有限责任公司"},
	})
	parsed := mustParseJSON(t, sanitizeOutput(out))
	for _, k := range []string{"base_url", "baseUrl", "apikey", "api_key", "app_code", "AppCode",
		"token", "authorization", "markdown_path", "supersedes_pcodes", "pcode", "Pcode"} {
		if _, ok := parsed[k]; ok {
			t.Fatalf("敏感键 %q 应被移除; parsed=%v", k, parsed)
		}
	}
	if parsed["data"].(map[string]any)["entName"] != "小米科技有限责任公司" {
		t.Fatalf("业务字段不应受影响; parsed=%v", parsed)
	}
}

// KEEPRISK_* 环境变量名出现在文案中应被替换（服务端配置名不外泄）。
func TestSanitizeOutputReplacesEnvNames(t *testing.T) {
	out := mustJSON(map[string]any{
		"error": "网络请求失败。请检查 KEEPRISK_API_BASE 与网络。",
	})
	parsed := mustParseJSON(t, sanitizeOutput(out))
	msg := parsed["error"].(string)
	if strings.Contains(msg, "KEEPRISK_") {
		t.Fatalf("环境变量名应被替换; msg=%q", msg)
	}
	if !strings.Contains(msg, "〔内部配置〕") {
		t.Fatalf("配置占位符应出现; msg=%q", msg)
	}
}

// URL 替换仅作用于元数据提示键（error/message/hint/warnings/reason/note），
// 业务数据键（data/payload）中的合法 URL（如企业官网）不受影响。
func TestSanitizeOutputURLScope(t *testing.T) {
	out := mustJSON(map[string]any{
		"message": "请求失败: https://api.internal.example:9080/api/data 连接超时",
		"hint":    "参考 https://docs.internal.example/guide",
		"data": map[string]any{
			"website": "https://www.xiaomi.com",
		},
		"payload": map[string]any{
			"site": "https://www.example-corp.com",
		},
	})
	parsed := mustParseJSON(t, sanitizeOutput(out))
	if strings.Contains(parsed["message"].(string), "https://") {
		t.Fatalf("元数据键中的 URL 应被替换; message=%q", parsed["message"])
	}
	if !strings.Contains(parsed["message"].(string), "〔网关地址〕") {
		t.Fatalf("网关地址占位符应出现; message=%q", parsed["message"])
	}
	if !strings.Contains(parsed["hint"].(string), "〔网关地址〕") {
		t.Fatalf("hint 键中的 URL 应被替换; hint=%q", parsed["hint"])
	}
	// 业务 URL 保留
	if got := parsed["data"].(map[string]any)["website"].(string); got != "https://www.xiaomi.com" {
		t.Fatalf("业务数据 URL 不应被替换; website=%q", got)
	}
	if got := parsed["payload"].(map[string]any)["site"].(string); got != "https://www.example-corp.com" {
		t.Fatalf("payload 业务 URL 不应被替换; site=%q", got)
	}
}
