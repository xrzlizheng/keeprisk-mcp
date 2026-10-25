package mcp

import (
	"encoding/json"
	"strings"
)

// 入口工具（resolve_entity / fuzzy_search / person_risk_profile 等）的参数兼容与
// 错误引导。
//
// 背景（2026-09-16 二轮盲测，3 个独立代理一致反馈）：
//   - 模型按其他工具的惯例传 ent_name，但入口工具的规范参数名是 keyword，
//     导致"首调即被 SDK schema 校验拒绝"，且报错只列字段名
//     （JSON-RPC -32602: required: missing properties），无业务语义与示例；
//   - 该摩擦占 trial3 统计的硬性试错的 75%。
//
// 处置：入口工具不再在 JSON Schema 层声明 required（SDK 会硬拒、无法自定义文案），
// 改为在 handler 内做"规范名 + 常见别名"归一，并对真正缺失的情况返回带
// 业务语义、示例与下一步指引的富错误。schema 的字段描述仍标注"必填"。

// entryAliases 是各入口工具的规范参数名 → 可接受别名（按优先级）。
// 别名覆盖模型惯用的 ent_name/company_name/name 体系，避免"惯例不匹配"型失败。
var entryAliases = map[string][]string{
	"keyword":     {"ent_name", "company_name", "name"},
	"person_name": {"name", "person", "person_keyword"},
}

// pickStrArg 取字符串参数：先按规范名，再按常见别名，最后做首尾空格清理。
func pickStrArg(args map[string]any, canonical string) string {
	if args == nil {
		return ""
	}
	if v := strArg(args, canonical); v != "" {
		return v
	}
	for _, alias := range entryAliases[canonical] {
		if v := strArg(args, alias); v != "" {
			return v
		}
	}
	return ""
}

// strArg 取单个字符串参数（非字符串类型不做隐式转换，避免把对象/数组误当主体）
func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// withWarning 在工具返回的 JSON 上追加一条警告（响应非 JSON 时原样返回）。
//
// 用途：对"传了但无效"的参数显式告知——静默忽略比报错更危险（模型会把结果当事实）。
func withWarning(out, warning string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return out
	}
	if parsed == nil {
		return out
	}
	if existing, ok := parsed["_warnings"].([]any); ok {
		parsed["_warnings"] = append(existing, warning)
	} else {
		parsed["_warnings"] = []any{warning}
	}
	return mustJSON(parsed)
}

// entryArgSpec 描述入口工具的必填参数（用于生成富错误）
type entryArgSpec struct {
	Tool      string         // 工具名
	Canonical string         // 规范参数名
	Meaning   string         // 业务语义（这个参数是什么）
	Example   map[string]any // 可直接照抄的完整示例
	Hint      string         // 下一步指引（可选）
}

// missingEntryArg 生成带业务语义、示例与别名说明的必填参数错误。
//
// 目标：让模型只读一次错误就能正确重试，而不是在"字段名猜谜"里反复失败。
func missingEntryArg(spec entryArgSpec) string {
	out := map[string]any{
		"error":           "validation",
		"message":         spec.Tool + " 缺少主体参数：请传 " + spec.Canonical + "（" + spec.Meaning + "）。",
		"accepted_fields": append([]string{spec.Canonical}, entryAliases[spec.Canonical]...),
	}
	if spec.Example != nil {
		out["example"] = spec.Example
	}
	if spec.Hint != "" {
		out["hint"] = spec.Hint
	}
	return mustJSON(out)
}
