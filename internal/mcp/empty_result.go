package mcp

import (
	"encoding/json"
	"fmt"
)

// 无数据（empty result）的统一标注。
//
// 背景（2026-09-17 用户指出）：网关接口"无数据"是**正常的成功响应**，因为有的查询
// 结果本身就是无数据（如某企业无被执行记录）。但响应形态为 `{"code":"110","data":null}`，
// 模型看到 `data: null` 容易读成"查询失败/服务故障"，进而重试、换参或下错误结论。
//
// 本文件在**工具输出统一出口**（registerTools 的 handler 返回处）做递归标注：
//   - 顶层无数据 → 加 `_empty_result:true` + `_note` + `_next`
//   - 聚合工具分节（`sections[].data.code=110`）→ 在该节内标注，并给出整体提示
//
// 放在统一出口的原因：聚合工具的分节包装分散在 7 个文件 20 处，
// 逐个修改易漏且难维护；出口集中处理可保证"无遗漏"。

// annotateEmptyResults 对工具输出做无数据标注（非 JSON 或无需标注时原样返回）。
func annotateEmptyResults(out string) string {
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return out
	}
	root, ok := v.(map[string]any)
	if !ok {
		return out
	}
	changed := annotateNode(root, 0)
	if !changed {
		return out
	}
	b, err := json.Marshal(root)
	if err != nil {
		return out
	}
	return string(b)
}

// annotateNode 递归遍历并标注无数据节点，返回是否有改动。
//
// visited 通过 depth 限制（避免深层递归开销过大），并跳过 _agent_meta 等元数据节点。
func annotateNode(node map[string]any, depth int) bool {
	if node == nil || depth > 6 {
		return false
	}
	changed := false

	// 1) 本节点自身是网关响应且无数据 → 标注
	//    注意：若 QueryImpl 已在更早阶段标注（_empty_result 存在），也要把本层视为
	//    "含无数据"，否则顶层聚合总结会漏生成（2026-09-17 实测发现：子节已有标记但
	//    changed 为 false，导致 _empty_hint 不出现）。
	if isEmptyResult(node) {
		if node["_empty_result"] == nil {
			node["_empty_result"] = true
			if _, exists := node["_empty_note"]; !exists {
				node["_empty_note"] = "查询成功但无记录（正常成功响应，非错误、非故障）；此结论可采信，无需重试或改参"
			}
		}
		changed = true
	}

	// 2) 递归子节点
	for k, child := range node {
		if k == "_agent_meta" || k == "_note" {
			continue // 元数据与说明不递归
		}
		switch c := child.(type) {
		case map[string]any:
			if annotateNode(c, depth+1) {
				changed = true
			}
		case []any:
			for _, item := range c {
				if m, ok := item.(map[string]any); ok {
					if annotateNode(m, depth+1) {
						changed = true
					}
				}
			}
		}
	}

	// 3) 聚合结果整体提示：仅当**子节点确实被标注**时，才在顶层给总结。
	//    注意：不能用"本节点是否无数据"判断（顶层通常不是无数据节点），
	//    也不能无条件调用 emptySummaryHint（其在无 sections 时会返回兜底文案，
	//    导致普通成功响应/错误响应被误加 _empty_hint —— 2026-09-17 修复）。
	if depth == 0 && changed {
		if _, exists := node["_empty_hint"]; !exists {
			if hint := emptySummaryHint(node); hint != "" {
				node["_empty_hint"] = hint
			}
		}
	}
	return changed
}

// emptySummaryHint 统计顶层 sections 中的无数据/有数据节数，生成总结提示。
func emptySummaryHint(root map[string]any) string {
	sections, ok := root["sections"].([]any)
	if !ok || len(sections) == 0 {
		return "本响应含「无记录」结果（见 _empty_result 标记）：属正常成功响应，不是错误；" +
			"在风险排查场景中「无记录」通常是有意义的正面结论。"
	}
	empty, withData := 0, 0
	for _, s := range sections {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		d, _ := m["data"].(map[string]any)
		if d != nil && d["_empty_result"] == true {
			empty++
		} else if m["status"] == "ok" {
			withData++
		}
	}
	if empty == 0 {
		return ""
	}
	return fmt.Sprintf("聚合结果：%d 个维度无记录（_empty_result 标记，属正常成功响应）、%d 个维度有数据；"+
		"无记录维度无需重试，可直接作为结论采信。", empty, withData)
}
