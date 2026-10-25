package mcp

// PythonVsGoParity 供 list_data_capabilities 返回，与 Python tool_parity.py 对齐。
func PythonVsGoParity() map[string]any {
	return map[string]any{
		"python_vs_go_tools": []map[string]string{
			{
				"python_only":   "list_product_lines",
				"go_equivalent": "list_categories",
				"go_args_hint":  "product_line 留空 → 产品线概览",
			},
			{
				"python_only":   "list_keeprisk_categories",
				"go_equivalent": "list_categories",
				"go_args_hint":  "product_line=<产品线>",
			},
			{
				"python_only":   "resolve_keeprisk_api",
				"go_equivalent": "search_keeprisk_apis",
				"go_args_hint":  `mode=resolve, intent="<意图描述>"`,
			},
			{
				// 2026-09-20：不再出现具体 Python 工具名——对照表曾被 Agent 当作可调用工具名
				// 去"补调"（本服务不存在该工具）。对照只保留能力差异结论。
				"python_only":   "（Python 版专属的 Markdown 文档读取能力，本服务不提供）",
				"go_equivalent": "(暂无)",
				"go_args_hint":  "接口参数细节用 get_keeprisk_api（支持编码或名称）+ keeprisk_query 直调",
			},
			{
				"python_only":   "enterprise_* 兼容别名",
				"go_equivalent": "search_keeprisk_apis / get_keeprisk_api",
				"go_args_hint":  "Python 保留 search_enterprise_apis 等；Go 统一 keeprisk 命名",
			},
		},
		"resources_parity": []map[string]string{
			{"uri": "keeprisk://catalog", "python": "yes", "go": "yes"},
			{"uri": "keeprisk://enterprise/catalog", "python": "yes", "go": "yes"},
		},
		"transport": map[string]string{
			"python":       "stdio；密钥经由 env/.env 注入",
			"go":           "HTTP POST /mcp；Header Authorization: Bearer <token>（401 无头）",
			"production":   "多租户/配额/审计请用 keeprisk-mcp-go",
		},
	}
}