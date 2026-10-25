# MCP Inspector 手测场景（P2）

在 Cursor / Claude Desktop 配置好 `AppCode` 后，用 MCP Inspector 或对话逐条验证。

## 场景 A — 供应商准入

1. `resolve_entity` keyword=目标企业
2. 用户确认候选后 `enterprise_credit_score`
3. 按评分：`enterprise_due_diligence_snapshot` sections=`basic,shareholders,admin_penalty,abnormal_operation,enforced,dishonest` digest_only=true

**通过标准**：无编造字段；code=110 表述为「无记录」。

## 场景 B — 产业链招商

1. `search_chains` keyword=人工智能
2. `get_chain_nodes` chain=人工智能
3. `search_chain_enterprises` chain=人工智能 province=江苏
4. 选 1 家 `investment_readiness_score`

## 场景 C — 快速信用预审

1. `fuzzy_search` keyword=统一社会信用代码或企业名
2. `company_quick_scan` depth=standard

## 场景 D — 成本控制

1. `estimate_billing` tool=enterprise_due_diligence_snapshot params=`{"include_judicial":false}`
2. `estimate_billing` tool=batch_query_companies params=`{"ent_names":["A","B"]}` → **estimated_calls=1**

## 场景 E — 参数构建（本地）

无需网关：`go test ./internal/mcp -run TestBuildEnterprise -v`