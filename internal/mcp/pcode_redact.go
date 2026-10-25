package mcp

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// pcodeKeys 是需要从 Agent/用户可见输出中整体移除的键（小写形式，匹配大小写不敏感）。
// 接口编码（如 E_007_0501）是 KeepRisk 内部机制；base_url/apikey/app_code/token 等
// 属基础设施地址与凭证信息，均不得出现在对外输出。
var pcodeKeys = map[string]struct{}{
	"pcode":             {},
	"pcode_used":        {},
	"pcode_raw":         {},
	"_redirect":         {},
	"supersedes_pcodes": {},
	"markdown_path":     {},
	"base_url":          {},
	"baseurl":           {},
	"api_key":           {},
	"apikey":            {},
	"app_code":          {},
	"appcode":           {},
	"secret":            {},
	"token":             {},
	"authorization":     {},
	"x_api_key":         {},
	"access_key":        {},
}

// internalPcodeRe 匹配 [EP]_ddd_dddd 形态的接口编码（示例 E_007_0501、P_003_0361）。
var internalPcodeRe = regexp.MustCompile(`[EP]_\d{3}_\d{4}`)

// internalEnvNameRe 匹配 KEEPRISK_ 前缀环境变量名（服务端配置名不外泄）。
var internalEnvNameRe = regexp.MustCompile(`KEEPRISK_[A-Z0-9_]+`)

// gatewayURLRe 匹配 http(s) URL。仅作用于元数据提示键的值：
// 错误/提示文案可能拼接网关地址；业务数据键（data/payload 等）中的 URL
// （如企业官网）是合法数据，刻意不替换，避免误伤。
var gatewayURLRe = regexp.MustCompile(`https?://[^\s"'，。；]+`)

// metaKeys URL 替换的作用域（提示/错误类键）。
// payload 是网关业务数据（可能含合法 URL），不在列。
var metaKeys = map[string]struct{}{
	"error":            {},
	"message":          {},
	"hint":             {},
	"retry_after_hint": {},
	"warnings":         {},
	"warning":          {},
	"reason":           {},
	"note":             {},
}

// sanitizeOutput 在统一出口清洗 Agent/用户可见输出：
//  1. 删除敏感键（递归、大小写不敏感）：pcode 系、supersedes_pcodes/markdown_path、
//     base_url/apikey/app_code/secret/token/authorization 等；
//  2. 字符串值中 [EP]_ddd_dddd 接口编码替换为「〔内部接口〕」、
//     KEEPRISK_* 环境变量名替换为「〔内部配置〕」（兜底覆盖错误文案、warnings 文本等）；
//  3. 元数据提示键（error/message/hint/warnings/reason/note 等）字符串中的
//     URL 替换为「〔网关地址〕」；业务数据中的 URL 保留。
//
// 对所有工具与 catalog 资源统一生效（无豁免）。目录类工具（search_keeprisk_apis 等）
// 输出不再展示接口编码，已持码调用方仍可用入参 pcode 直调（输入不属于输出）。
//
// 默认开启；KEEPRISK_HIDE_PCODE=false 可关闭（仅调试用，关闭时启动日志告警）。
// 审计日志不受影响：本函数在审计记录落笔后的输出阶段应用。
func sanitizeOutput(out string) string {
	if pcodeRedactionDisabled() {
		return out
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		// 非 JSON（极少）：对字符串做编码与环境变量名替换兜底
		return internalEnvNameRe.ReplaceAllString(
			internalPcodeRe.ReplaceAllString(out, "〔内部接口〕"), "〔内部配置〕")
	}
	cleaned := redactValue(v, false)
	b, err := json.Marshal(cleaned)
	if err != nil {
		return out
	}
	return string(b)
}

func pcodeRedactionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KEEPRISK_HIDE_PCODE")))
	return v == "false" || v == "0"
}

// redactValue 递归清洗：meta=true 时（处于元数据提示键作用域内）字符串额外做 URL 替换。
func redactValue(v any, meta bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			lk := strings.ToLower(k)
			if _, drop := pcodeKeys[lk]; drop {
				continue
			}
			_, isMeta := metaKeys[lk]
			out[k] = redactValue(vv, isMeta)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = redactValue(vv, meta)
		}
		return out
	case string:
		s := internalPcodeRe.ReplaceAllString(t, "〔内部接口〕")
		s = internalEnvNameRe.ReplaceAllString(s, "〔内部配置〕")
		if meta {
			s = gatewayURLRe.ReplaceAllString(s, "〔网关地址〕")
		}
		return s
	default:
		return v
	}
}
