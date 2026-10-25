package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xrzlizheng/keeprisk-mcp/internal/audit"
	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
	"github.com/xrzlizheng/keeprisk-mcp/internal/cache"
	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
	"github.com/xrzlizheng/keeprisk-mcp/internal/keeprisk"
	"github.com/xrzlizheng/keeprisk-mcp/internal/params"
	"github.com/xrzlizheng/keeprisk-mcp/internal/quota"
)

// serverInstructions 每次 initialize 都会下发（约 2.4KB）。
// 2026-09-20 瘦身（6.2KB→2.4KB）：本文件是**每会话固定 token 成本**，与 tools/list
// 清单（~33KB）叠加构成会话底座。保留高价值护栏（无数据语义/反幻觉/鉴权/成本），
// 场景编排压缩为单行（完整剧本在 list_data_capabilities 的 agent_playbook，按需拉取），
// 删除逐工具清单（工具描述已在 tools/list 里，重复下发纯浪费）。
const serverInstructions = `KeepRisk 企业数据 MCP：235 接口，47 个意图级工具。

【无数据是正常结果】网关 code=110 或空 data = "查询成功但该主体无此类记录"，不是错误/故障。看到 _empty_result:true：不要重试或改参，可直接作为结论（风险排查中"无记录"常是正面信号）。怀疑主体名写法问题 → 用 resolve_entity 核验，而非换参重试。

【反幻觉】仅引用工具返回字段；code=110 是无记录不是失败；主体不明先 resolve_entity。

【鉴权】HTTP Header: Authorization: <token>（兼容 Bearer 前缀）。

【首要工具】company_quick_scan（一句话查企业）｜enterprise_due_diligence_snapshot（26维尽调，sections 子集/include_judicial/digest_only）｜enterprise_credit_score（信用门卫）｜fuzzy_search / resolve_entity（找企业）｜person_risk_profile（查人员）｜enterprise_graph_relations（关联图谱，默认摘要模式）｜list_data_capabilities（30+ 场景剧本）｜estimate_billing（0 成本预估）｜keeprisk_query（235 接口兜底直调，pcode 支持接口名称）。

【语义提醒】actual_controller=向下（该企业控制的下游企业清单）；向上 UBO 用 enterprise_extended_profile.beneficial_owner。judicial_deep_dive 仅覆盖司法协助+破产（见其 scope 字段）。工具输出不含内部接口编码，接口名称是直调唯一句柄。

【成本】尽调每节=1 计费单位；digest_only=true 省 token；缓存命中 0 网关调用。先 digest 扫描、命中风险再查明细。

【典型编排】A 供应商准入：resolve_entity → credit_score → 按分值走轻量/标准/深度尽调。B 司法排查：snapshot(include_judicial=true, digest_only=true) → red 节查原始数据。C 上市公司：listed_company_profile → 尽调。D 成本敏感：estimate_billing → digest_only 扫描 → 按需取数。更多场景见 list_data_capabilities。
`

type Services struct {
	Cat   *catalog.Catalog
	KR    *keeprisk.Client
	Quota *quota.Manager
	Cache *cache.Cache
	// QueryFn 测试注入点：非 nil 时聚合工具内部调用走该函数而非 QueryImpl。
	// 生产环境保持 nil；测试在调用前赋值（单测串行执行，无并发读写）。
	QueryFn func(ctx context.Context, pcode string, params map[string]any) string
}

// callQuery 聚合工具内部网关调用入口：优先走测试注入的 QueryFn，否则走 QueryImpl。
func (s *Services) callQuery(ctx context.Context, pcode string, params map[string]any) string {
	if s.QueryFn != nil {
		return s.QueryFn(ctx, pcode, params)
	}
	return s.QueryImpl(ctx, pcode, params)
}

func NewServer(svc *Services) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "keeprisk-enterprise", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: serverInstructions},
	)
	registerTools(server, svc)
	registerResources(server, svc)
	return server
}

func registerResources(server *mcp.Server, svc *Services) {
	server.AddResource(&mcp.Resource{
		URI:         "keeprisk://catalog",
		Name:        "keeprisk-catalog",
		Description: "全量最新接口目录 JSON（不含内部接口编码）",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		items := svc.Cat.LatestAPIs()
		// 目录只保留语义化元数据：pcode/supersedes_pcodes/markdown_path 等编码类字段不外露
		slim := make([]map[string]any, 0, len(items))
		for _, x := range items {
			slim = append(slim, map[string]any{
				"title":        x.Title,
				"product_line": x.ProductLine,
				"category":     x.Category,
			})
		}
		b, _ := json.MarshalIndent(slim, "", "  ")
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "keeprisk://catalog", MIMEType: "application/json", Text: sanitizeOutput(string(b))}}}, nil
	})
}

// appendArgWarnings 把"未知入参"提示合并进工具输出 JSON 的 warnings 数组。
// 仅当输出是 JSON 对象时注入；单次最多提示 2 条，避免反向噪音。
func appendArgWarnings(schema map[string]any, args map[string]any, out string) string {
	warns := unknownArgWarnings(schema, args)
	if len(warns) == 0 {
		return out
	}
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil || m == nil {
		return out
	}
	if _, hasErr := m["error"]; hasErr {
		return out // 错误响应已自带指引，不叠加
	}
	existing, _ := m["warnings"].([]any)
	for _, w := range warns {
		existing = append(existing, w)
	}
	m["warnings"] = existing
	return mustJSON(m)
}

// unknownArgWarnings 返回 schema 未声明却被传入的参数的提示。
// 对疑似主体字段别名（company_name/entName/companyName 等）给出本工具实际参数名，
// 其余未知参数列出可用参数清单（截断到 8 个）。
func unknownArgWarnings(schema map[string]any, args map[string]any) []string {
	known := schemaPropertyNames(schema)
	if len(known) == 0 || len(args) == 0 {
		return nil
	}
	// 别名 → 本工具可能的规范参数（命中其一才给定向提示）
	aliasMap := map[string][]string{
		"company_name": {"ent_name", "company_id", "keyword", "chain_name", "park_name"},
		"companyname":  {"ent_name", "company_id", "keyword", "chain_name", "park_name"},
		"entname":      {"ent_name", "company_id", "keyword"},
		"ent_name":     {"ent_name", "company_id", "keyword"},
		"company":      {"ent_name", "company_id", "keyword"},
		"person":       {"person_name"},
		"personname":   {"person_name"},
		"person_name":  {"person_name", "ent_name"},
	}
	var warns []string
	for k := range args {
		if known[k] {
			continue
		}
		if len(warns) >= 2 {
			break
		}
		lk := strings.ToLower(k)
		if canonicals, ok := aliasMap[lk]; ok {
			for _, c := range canonicals {
				if known[c] {
					warns = append(warns, fmt.Sprintf("参数 %s 不在本工具入参中（已被忽略）；本工具用 %q，请改用该名称重试", k, c))
					break
				}
			}
			continue
		}
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 8 {
			names = append(names[:8], "…")
		}
		warns = append(warns, fmt.Sprintf("参数 %s 不在本工具入参中（已被忽略）；可用参数：%s", k, strings.Join(names, ", ")))
	}
	return warns
}

// schemaPropertyNames 收集 schema 顶层属性名（含 anyOf 各分支——主体选择器 schema 用 anyOf 组织）。
func schemaPropertyNames(schema map[string]any) map[string]bool {
	names := map[string]bool{}
	var walk func(s map[string]any)
	walk = func(s map[string]any) {
		if props, ok := s["properties"].(map[string]any); ok {
			for k := range props {
				names[k] = true
			}
		}
		if anyOf, ok := s["anyOf"].([]any); ok {
			for _, b := range anyOf {
				if bm, ok := b.(map[string]any); ok {
					walk(bm)
				}
			}
		}
	}
	walk(schema)
	return names
}

func registerTools(server *mcp.Server, svc *Services) {
	// add 注册一个工具；annotations 可为 nil；schema 为 JSON Schema（nil 时用空 object，避免 go-sdk v0.8 对 nil schema panic）
	add := func(name, desc string, annotations *mcp.ToolAnnotations, schema map[string]any, handler func(context.Context, map[string]any) (string, error)) {
		if schema == nil {
			schema = schemaEmptyObject()
		}
		// Out 用 any：避免 go-sdk 从 string 推断非 object 的 outputSchema 导致 panic
		mcp.AddTool[map[string]any, any](server, &mcp.Tool{Name: name, Description: desc, Annotations: annotations, InputSchema: schema}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			// 注入工具名与网关调用计数器：网关层审计据此归属工具并统计实际调用次数
			ctx = audit.WithTool(audit.WithGatewayCounter(ctx), name)
			start := time.Now()
			out, err := handler(ctx, args)
			auditToolCall(ctx, name, args, out, err, time.Since(start))
			if err != nil {
				// 错误文案也经统一出口清洗（err.Error() 可能拼入 pcode/网关地址）
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: sanitizeOutput(mustJSON(map[string]any{"error": err.Error()}))}}}, nil, nil
			}
			// 未知参数告警（2026-09-20）：schema 未声明却被传入的参数会被静默忽略——
			// 实测 person_risk_profile 传 company_name（schema 为 ent_name）时
			// executives_at_company 整节无声消失，模型无从得知。此处统一在出口提示。
			out = appendArgWarnings(schema, args, out)
			// 审计已落笔（内部留痕保留 pcode）；输出前统一脱敏（所有工具，无豁免）
			// 统一出口附加"无数据"标注：覆盖聚合工具的分节（嵌套 data.code=110），
			// 避免模型把 data:null 读成失败（详见 annotateEmptyResults）
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: annotateEmptyResults(sanitizeOutput(out))}}}, nil, nil
		})
	}

	// openWorld = 调用外部 KeepRisk 网关的工具
	openWorld := &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: boolPtr(true)}
	// readOnly = 仅本地操作（catalog/校验/配额查询），不调网关
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}

	add("keeprisk_query", "调用 KeepRisk POST /api/data。pcode 可传接口编码或接口名称（search_keeprisk_apis 返回的 title，如「院校信息」）。params 为业务参数字典。已知参数传法要求会在调用前拦截（返回 validation 错误并给出正确写法）或随成功响应以 warnings 返回——请务必查看 warnings，它提示的可能正是「参数被上游忽略、结果未按预期过滤」。注意：返回 code=110 或空 data 表示「查询成功但无记录」，是**正常成功响应而非错误**（响应带 _empty_result 标记与说明），不要因此重试、改参或判定服务异常。", openWorld, schemaKeepriskQuery(), func(ctx context.Context, args map[string]any) (string, error) {
		pcode, _ := args["pcode"].(string)
		var p map[string]any
		if raw, ok := args["params"]; ok && raw != nil {
			var okMap bool
			p, okMap = raw.(map[string]any)
			if !okMap {
				return mustJSON(map[string]any{"error": "validation", "message": "params 必须是 object（字典）类型"}), nil
			}
		}
		return svc.QueryImpl(ctx, pcode, p), nil
	})

	add("enterprise_due_diligence_snapshot", "企业尽调/风险扫描（26维度并行聚合）：sections 逗号分隔选择子集，include_judicial=false 关闭司法节节省计费，digest_only=true 仅返回摘要减少 token。每节含 digest（severity/risk_level/summary）。", openWorld, schemaDueDiligence(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		sections, _ := args["sections"].(string)
		inc := true
		if v, ok := args["include_judicial"].(bool); ok {
			inc = v
		}
		mc := intArg(args, "max_concurrency", 4)
		digestOnly, _ := args["digest_only"].(bool)
		return svc.DueDiligenceSnapshot(ctx, ent, cid, sections, inc, mc, digestOnly), nil
	})

	add("list_data_capabilities", "列出 MCP 数据能力：47 个工具清单、六大域 domain_playbook 工具地图、Python/Go 工具对照、配额/缓存统计与典型业务场景（本地目录，不调用网关、不计费）。", readOnly, nil, func(ctx context.Context, args map[string]any) (string, error) {
		_ = ctx
		_ = args
		return svc.ListCapabilities(), nil
	})

	add("quota_status", "当前账户的配额/限流/并发状态（不消耗网关调用）", readOnly, nil, func(ctx context.Context, _ map[string]any) (string, error) {
		ac := auth.AppCodeFromContext(ctx)
		if ac == "" {
			return mustJSON(map[string]any{"error": "unauthenticated"}), nil
		}
		st := svc.Quota.Status(ac)
		// 账户标识不回传（内部按真实 AppCode 隔离统计）；st 再经出口清洗兜底删 app_code 键
		st.AppCode = ""
		return sanitizeOutput(mustJSON(st)), nil
	})

	add("enterprise_basic_profile", "快捷查询企业工商照面：返回工商全量核心字段（法人/信用码/注册资金/行业/状态/地址/经营范围/上市信息等），同时返回内部 id（可用于 companyId 类查询，如 school_alumni_circle 的 company_name 入口内部即取此 id 反查人员学历）。ent_name 或 company_id 二选一。", openWorld, schemaBasicProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		if strings.TrimSpace(ent) == "" && strings.TrimSpace(cid) == "" {
			return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"}), nil
		}
		p := map[string]any{}
		if strings.TrimSpace(cid) != "" {
			p["companyId"] = strings.TrimSpace(cid)
		}
		if strings.TrimSpace(ent) != "" {
			p["entName"] = strings.TrimSpace(ent)
		}
		return svc.QueryImpl(ctx, "E_007_0501", p), nil
	})

	// enterprise_credit_score — 企业信用评分：低成本"门卫"工具
	// 并行聚合 6 个评分/角标/标签接口，输出四维评分 + 风险等级 + 决策建议
	// 用于快速预判信用等级，决定后续尽调深度
	add("enterprise_credit_score", "企业信用评分（6接口并行聚合）：四维评分（财务/经营/科创/综合）+ 数据角标 + 企业标签 + 风险等级 + 决策建议。低成本门卫工具（6次API，~1-2s），≥80快速通过/50-79标准尽调/<50深度排查。ent_name 或 company_id 二选一。", openWorld, schemaCreditScore(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.EnterpriseCreditScore(ctx, ent, cid), nil
	})

	// resolve_entity — 主体锚定：将模糊企业名解析为候选列表
	// 底层调用 E_007_1331（企业筛选V4），提取结构化候选信息供 Agent 选择
	// 验收标准：两个同名企业必须返回 ≥2 候选，要求 Agent 让用户确认
	add("resolve_entity", "主体锚定：将模糊/简称/曾用名解析为企业候选列表（含 ent_name/company_id/credit_code/法人/省市/状态/匹配提示）。Agent 应先调此工具确认主体后再调尽调/穿透等工具，避免同名串户。region 可缩小范围。主体参数传 keyword（也接受 ent_name/company_name/name 别名）。", openWorld, schemaResolveEntity(), func(ctx context.Context, args map[string]any) (string, error) {
		keyword := pickStrArg(args, "keyword")
		region, _ := args["region"].(string)
		limit := intArg(args, "limit", 5)
		if keyword == "" {
			return missingEntryArg(entryArgSpec{
				Tool: "resolve_entity", Canonical: "keyword",
				Meaning: "企业名称关键词（全称/简称/曾用名均可）",
				Example: map[string]any{"keyword": "安徽智侒信信息技术有限公司"},
				Hint:    "也可用 fuzzy_search 做企业名/信用代码/电话/人名的统一模糊检索。",
			}), nil
		}
		return svc.ResolveEntity(ctx, keyword, region, limit), nil
	})

	add("fuzzy_search", "统一模糊搜索（Agent-Native 入口）：企业名/信用代码/电话/人名自动路由，对齐企查查 FuzzySearch。主体参数传 keyword（也接受 ent_name/company_name/name 别名）。", openWorld, schemaFuzzySearch(), func(ctx context.Context, args map[string]any) (string, error) {
		kw := pickStrArg(args, "keyword")
		region, _ := args["region"].(string)
		limit := intArg(args, "limit", 5)
		if kw == "" {
			return missingEntryArg(entryArgSpec{
				Tool: "fuzzy_search", Canonical: "keyword",
				Meaning: "检索关键词：企业名/统一社会信用代码/电话/人名",
				Example: map[string]any{"keyword": "安徽智侒信信息技术有限公司"},
				Hint:    "已知是企业名时也可直接用 resolve_entity 做主体锚定。",
			}), nil
		}
		return svc.FuzzySearch(ctx, kw, region, limit, fieldsArg(args)), nil
	})

	add("company_quick_scan", "一句话查企业：工商照面+信用评分+风险digest 一次返回。depth=light|standard|deep。对齐企查查「查工商+风险」开箱体验。", openWorld, schemaCompanyQuickScan(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		kw, _ := args["keyword"].(string)
		depth, _ := args["depth"].(string)
		return svc.CompanyQuickScan(ctx, ent, cid, kw, depth), nil
	})

	add("compare_enterprises", "多企业横向对比（2-5家）：并行信用评分+精简风险digest，返回 ranking。适合供应商比选、投标方对比。", openWorld, schemaCompareEnterprises(), func(ctx context.Context, args map[string]any) (string, error) {
		var ents []string
		if v, ok := args["ent_names"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					ents = append(ents, s)
				}
			}
		}
		var cids []string
		if v, ok := args["company_ids"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					cids = append(cids, s)
				}
			}
		}
		return svc.CompareEnterprises(ctx, ents, cids), nil
	})

	add("person_risk_profile", "董监高/人员风险画像：按 person_name（必填，也接受 name 别名）查人员模糊匹配+被执行+失信记录。风险记录按人名检索，ent_name 仅用于附加返回该公司的任职名单（不参与过滤）——同名他人记录会一并返回，需结合公司任职名单/地区自行消歧。对齐企查查 qcc-executive 域。", openWorld, schemaPersonRiskProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		pn := pickStrArg(args, "person_name")
		ent, _ := args["ent_name"].(string)
		if pn == "" {
			return missingEntryArg(entryArgSpec{
				Tool: "person_risk_profile", Canonical: "person_name",
				Meaning: "人员姓名（如 陈康康）",
				Example: map[string]any{"person_name": "陈康康", "ent_name": "安徽智侒信信息技术有限公司"},
				Hint:    "ent_name 为可选：用于附加该公司任职名单以判断同名是否同一人。",
			}), nil
		}
		return svc.PersonRiskProfile(ctx, pn, ent), nil
	})

	add("entity_risk_timeline", "企业风险/变更时间线：并行拉取工商变更、行政处罚、立案、经营异常并合并排序。对齐企查查 qcc-history 轻量版。", openWorld, schemaEntityRiskTimeline(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		limit := intArg(args, "limit", 20)
		return svc.EntityRiskTimeline(ctx, ent, cid, limit), nil
	})

	// 聚合工具注册已拆分至 tools_aggregate.go
	registerAggregateTools(add, svc, openWorld, readOnly)

	add("batch_query_companies", "批量工商核验：ent_names 或 company_ids 数组，最多 100 家。返回工商核心字段。适合供应商/客户名单批量核验。", openWorld, schemaBatchQuery(), func(ctx context.Context, args map[string]any) (string, error) {
		var ents []string
		if v, ok := args["ent_names"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					ents = append(ents, strings.TrimSpace(s))
				}
			}
		}
		var cids []string
		if v, ok := args["company_ids"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					cids = append(cids, strings.TrimSpace(s))
				}
			}
		}
		if len(ents)+len(cids) == 0 {
			return mustJSON(map[string]any{"error": "请提供 ent_names 或 company_ids 数组（最多 100 条）"}), nil
		}
		if len(ents)+len(cids) > 100 {
			return mustJSON(map[string]any{
				"error":      "validation",
				"message":    "批量上限 100 条",
				"received":   len(ents) + len(cids),
				"max_limit":  100,
				"hint":       "请分批调用（按业务可拆 1-2 次）。",
			}), nil
		}
		p := map[string]any{"pageNo": intArg(args, "page", 1), "pageSize": intArg(args, "page_size", 100)}
		if len(ents) > 0 {
			p["entNames"] = ents
		}
		if len(cids) > 0 {
			p["companyIdList"] = cids
		}
		return svc.QueryImpl(ctx, "E_007_1111", p), nil
	})

	add("control_penetration", "股权穿透：以 ent_name 为起点返回股权关系，含 investorList（对外投资）和 holderList（直接股东）。仅支持企业名称查询。", openWorld, schemaControlPenetration(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		if strings.TrimSpace(ent) == "" {
			return mustJSON(map[string]any{"error": "请提供 ent_name（必填）"}), nil
		}
		return svc.QueryImpl(ctx, "E_007_0621", map[string]any{"entName": strings.TrimSpace(ent)}), nil
	})

	add("actual_controller", "实际控制权（方向：向下/对外）：列出目标企业**控制的下游企业清单**（对外投资/控制路径，含持股比例、注册资本、登记状态）。不回答\"谁控制这家公司\"——向上实控人/最终受益人（UBO）请用 enterprise_extended_profile 的 beneficial_owner 节。仅支持 ent_name 查询。", openWorld, schemaActualController(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		if strings.TrimSpace(ent) == "" {
			return mustJSON(map[string]any{"error": "请提供 ent_name（必填）"}), nil
		}
		p := map[string]any{"entName": strings.TrimSpace(ent), "pageNo": intArg(args, "page", 1), "pageSize": intArg(args, "page_size", 10)}
		raw := svc.QueryImpl(ctx, "E_007_0201", p)
		// 方向标注：2026-09-20 修正。该接口载荷是"本企业控制的下游企业"（items 为对外
		// 投资清单），旧描述"识别实控人/控制链"把模型引到完全相反的方向——真正的 UBO
		// 在 extended_profile.beneficial_owner（实测：本工具返回 16 家子公司，UBO 李坤在别处）。
		if m, ok := jsonRaw(raw).(map[string]any); ok {
			m["direction"] = "outbound"
			m["_note"] = "direction=outbound：本清单是目标企业控制的下游企业（向外）。向上实控人/最终受益人请用 enterprise_extended_profile（beneficial_owner 节，含穿透链）"
			return mustJSON(m), nil
		}
		return raw, nil
	})

	add("search_chain_enterprises", "产业链企业筛选：按产业链名称+地区/节点/状态/资质/财务等条件筛选链上企业。必填 chain。详见 schema。", openWorld, schemaSearchChainEnterprises(), func(ctx context.Context, args map[string]any) (string, error) {
		chain, _ := args["chain"].(string)
		if strings.TrimSpace(chain) == "" {
			return mustJSON(map[string]any{"error": "请提供 chain（产业链名称，必填）"}), nil
		}
		return svc.QueryImpl(ctx, "E_007_1131", buildChainSearchParams(args)), nil
	})

	add("get_chain_nodes", "产业链节点树：传入 chain（产业链名称，必填）返回该产业链的完整节点层级树，含节点名称、节点编码、父子关系等，用于产业链招商前了解产业链结构。", openWorld, schemaGetChainNodes(), func(ctx context.Context, args map[string]any) (string, error) {
		chain, _ := args["chain"].(string)
		chain = ensureChainSuffix(chain)
		return svc.QueryImpl(ctx, "E_007_1141", map[string]any{"chainHis": chain}), nil
	})

	add("search_chains", "产业链模糊搜索：按 keyword（产业链名称关键词，必填）模糊搜索产业链列表，返回产业链名称、编码等基础信息。可选 data_type（0通用/1项目定制，默认0）。支持 page/page_size 分页，默认 1/10。", openWorld, schemaSearchChains(), func(ctx context.Context, args map[string]any) (string, error) {
		kw, _ := args["keyword"].(string)
		kw = strings.TrimSpace(kw)
		if kw == "" {
			return mustJSON(map[string]any{"error": "validation", "message": "缺少必填参数 keyword（产业链名称关键词）"}), nil
		}
		page := intArg(args, "page", 1)
		ps := intArg(args, "page_size", 10)
		p := map[string]any{"keyword": kw, "pageNo": page, "pageSize": ps}
		if dt := intArg(args, "data_type", 0); dt > 0 {
			p["dataType"] = dt
		}
		return svc.QueryImpl(ctx, "E_011_0381", p), nil
	})

	add("search_enterprises", "多条件企业筛选：按关键词/地区/行业/状态/上市/融资/科技资质等条件筛选企业。详见 schema 参数说明。", openWorld, schemaSearchEnterprises(), func(ctx context.Context, args map[string]any) (string, error) {
		return svc.QueryImpl(ctx, "E_007_1331", buildEnterpriseSearchParams(args)), nil
	})

	// list_categories：合并自旧版 list_product_lines + list_keeprisk_categories
	// product_line 为空时返回全部产品线概览 + 全部分类；非空时返回该产品线下的分类
	add("list_categories", "浏览接口目录：product_line 为空返回产品线概览+分类样本；非空按产品线过滤分类。", readOnly, schemaListCategories(), func(ctx context.Context, args map[string]any) (string, error) {
		pl, _ := args["product_line"].(string)
		if strings.TrimSpace(pl) == "" {
			return mustJSON(map[string]any{
				"total_apis":   len(svc.Cat.LatestAPIs()),
				"product_lines": svc.Cat.ProductLines(),
				"categories_sample": svc.Cat.Categories(""),
			}), nil
		}
		cats := svc.Cat.Categories(pl)
		return mustJSON(map[string]any{
			"product_line": pl,
			"count":        len(cats),
			"categories":   cats,
		}), nil
	})

	// school_alumni_circle — 校友圈企业：校企域复合工具（把未文档化链路收口为一次调用）
	add("school_alumni_circle", "校友圈企业（校企）：双入口——① university_name（院校官方全称，如 中国科学技术大学；简称如 中科大 会自动解析，+1 次网关调用）；② company_name（企业名称，由其法人院校反查）。返回企业+校友姓名/职位/毕业院校；上游 text 过滤不严格，本工具已对校友学历做二次校验、只保留真实命中。区划：用 province（必填锚点）/city/county 筛选，内部构造上游要求的 regionList 数组（⚠️实测平铺传参会被上游静默忽略且照常计费，故必须经本工具传）。分页：用 page（起始页，默认 1）与 max_pages（连续页数，默认 1、最大 10、每页 1 次网关调用）扫描多页；响应含 pages_scanned/upstream_total/_sampling。法人无学历数据时返回该公司有学历记录的人员列表，不返回空结果。include_stats=true 附带地域/行业分布（+1 次）。如需先锚定主体请用 resolve_entity。", openWorld, schemaSchoolAlumniCircle(), func(ctx context.Context, args map[string]any) (string, error) {
		stats := false
		if v, ok := args["include_stats"].(bool); ok {
			stats = v
		}
		name, _ := args["university_name"].(string)
		company, _ := args["company_name"].(string)
		return svc.SchoolAlumniCircle(ctx, SchoolAlumniOpts{
			UniversityName: name,
			CompanyName:    company,
			Province:       pickStrArg(args, "province"),
			City:           strArg(args, "city"),
			County:         strArg(args, "county"),
			PageSize:       intArg(args, "page_size", 100),
			Page:           intArg(args, "page", 1),
			MaxPages:       intArg(args, "max_pages", 1),
			IncludeStats:   stats,
		}), nil
	})

	// search_keeprisk_apis：合并自旧版 search + resolve
	// mode=search（默认）| resolve；resolve 走意图匹配，返回推荐+备选
	add("search_keeprisk_apis", "搜索/匹配接口：mode=search 按 query 关键词；mode=resolve 按 intent 自然语言意图。部分接口返回 param_requirements 字段（实测确认的参数要求：必填/容器格式/会被静默忽略的参数及正确写法），调用前务必查看。", readOnly, schemaSearchApis(), func(ctx context.Context, args map[string]any) (string, error) {
		mode, _ := args["mode"].(string)
		pl, _ := args["product_line"].(string)
		if mode == "resolve" {
			intent, _ := args["intent"].(string)
			return mustJSON(svc.Cat.ResolveAPI(intent, pl)), nil
		}
		q, _ := args["query"].(string)
		cat, _ := args["category"].(string)
		limit := intArg(args, "limit", 25)
		items := svc.Cat.Search(q, cat, pl, limit)
		apis := make([]map[string]any, 0, len(items))
		for i := range items {
			apis = append(apis, catalog.Slim(&items[i]))
		}
		return mustJSON(map[string]any{"total_shown": len(apis), "apis": apis}), nil
	})

	add("get_keeprisk_api", "获取单个接口元数据：按 pcode 或接口名称查询业务参数（名称+提示）、必填组（required_one_of）、入参 schema、示例请求与多版本弃用信息。若返回 param_requirements 字段，表示该接口存在实测确认的参数传法要求（如区划须用 regionList 数组而平铺会被静默忽略）——请严格按其中的 fix 写法传参。名称用 search_keeprisk_apis 检索到的 title（如「院校信息」）。直调 keeprisk_query 前建议先调用确认主体字段名（entName/name/text/companyName/keyword/chainHis 因接口而异）。本地目录查询，不调用网关、不计费。", readOnly, schemaGetApi(), func(ctx context.Context, args map[string]any) (string, error) {
		pcode, _ := args["pcode"].(string)
		item, verr := svc.resolveAPIRef(pcode)
		if item == nil {
			return mustJSON(map[string]any{"error": verr}), nil
		}
		out := catalog.Slim(item)
		out["business_params"] = item.BusinessParams
		// input_schema 的空 description 已在 catalog.LoadJSON 加载期统一清理
		out["input_schema"] = item.InputSchema
		// 原始文档参数元数据（含枚举值定义）：模型需要知道 "9=吊销" 这类取值含义
		if len(item.DocParams) > 0 {
			out["doc_params"] = item.DocParams
		}
		if item.ExampleFromDoc {
			out["example_source"] = "接口原始文档（权威示例，含主体参数）"
		}
		return mustJSON(out), nil
	})

	add("validate_keeprisk_params", "本地校验 keeprisk_query 入参：pcode 可传接口编码或接口名称。检查必填组/必填字段、归一化参数名（ent_name→entName 等别名）、并校验实测确认的参数要求（param_requirements：容器格式、会被上游静默忽略的参数），返回 valid/errors/warnings/example_request。不调用网关、不计费，强烈建议直调前先校验——被静默忽略的参数会返回错误数据且照常计费。", readOnly, schemaValidateParams(), func(ctx context.Context, args map[string]any) (string, error) {
		pcode, _ := args["pcode"].(string)
		var p map[string]any
		if raw, ok := args["params"]; ok {
			p, _ = raw.(map[string]any)
		}
		item, verr := svc.resolveAPIRef(pcode)
		if item == nil {
			return mustJSON(map[string]any{"valid": false, "errors": []string{verr}}), nil
		}
		norm, errs, warns := params.Validate(item, p)
		return mustJSON(map[string]any{
			"valid":           len(errs) == 0,
			"pcode_used":      item.Pcode,
			"normalized":      norm,
			"errors":          errs,
			"warnings":        warns,
			"example_request": item.ExampleRequest,
		}), nil
	})
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return def
	}
}

// auditToolCall 记录一次 MCP 工具调用的审计日志
//
// 提取规则：
//   - pcode_raw：用户传入的 pcode（如适用）
//   - pcode：实际生效的 pcode（从响应中 pcode_used 字段提取；历史编码本地拒绝、不会出现在此）
//   - subject：ent_name/company_id 不入日志，仅入 hash + length
//   - status：ok / error / validation / config（与 QueryImpl 错误分类对齐）
func auditToolCall(ctx context.Context, tool string, args map[string]any, output string, err error, latency time.Duration) {
	r := audit.Default().FromContext(ctx)
	r.Method = "tools/call"
	r.Tool = tool
	r.LatencyMS = latency.Milliseconds()
	r.RespBytes = len(output)

	// 实际发出的网关调用次数（计费单位还原依据，成功失败均计）。
	// 计数器由本函数所在调用链在 handleToolCall 中注入；未注入时不写该字段，
	// 避免把"未埋点"误读为"零成本"。
	if n, ok := audit.GatewayCountFromContext(ctx); ok {
		r.SetExtra("gateway_calls", n)
	}

	// 提取入参中的查询主体（hash 化）
	if ent, ok := args["ent_name"].(string); ok && strings.TrimSpace(ent) != "" {
		r.SubjectHash = audit.HashSubject(ent)
		r.SubjectKind = audit.SubjectKind(ent, _getStr(args, "company_id"))
		r.SubjectLen = len(strings.TrimSpace(ent))
	} else if cid, ok := args["company_id"].(string); ok && strings.TrimSpace(cid) != "" {
		r.SubjectHash = audit.HashSubject(cid)
		r.SubjectKind = audit.SubjectKind("", cid)
		r.SubjectLen = len(strings.TrimSpace(cid))
	}

	// pcode_raw 入参
	if pc, ok := args["pcode"].(string); ok && pc != "" {
		r.PcodeRaw = pc
	}

	// 从响应中提取 pcode_used 和 error
	if output != "" {
		var parsed map[string]any
		if jerr := json.Unmarshal([]byte(output), &parsed); jerr == nil {
			if v, ok := parsed["pcode_used"].(string); ok {
				r.Pcode = v
			}
			if errStr, ok := parsed["error"].(string); ok && errStr != "" {
				r.Status = errStr
				if msg, ok := parsed["message"].(string); ok {
					r.ErrorMsg = msg
				}
				if code, ok := parsed["code"]; ok {
					r.ErrorCode = fmt.Sprint(code)
				}
			} else {
				r.Status = "ok"
			}
			// billing_units（尽调快照）：用 SetExtra 合并，避免覆盖 gateway_calls
			if bu, ok := parsed["billing_units"]; ok {
				r.SetExtra("billing_units", bu)
			}
		} else {
			r.Status = "parse_error"
		}
	} else if err != nil {
		r.Status = "error"
		r.ErrorMsg = err.Error()
	} else {
		r.Status = "ok"
	}

	audit.Default().Record(r)
}

func _getStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func ensureChainSuffix(chain string) string {
	chain = strings.TrimSpace(chain)
	if chain == "" {
		return chain
	}
	if !strings.HasSuffix(chain, "产业链") {
		return chain + "产业链"
	}
	return chain
}

func ServerInstructions() string { return serverInstructions }

// Export for main
func MustServices() (*Services, error) {
	// 脱敏关闭属高危状态（对外输出将出现接口编码/配置名），启动即显著告警（仅调试用）
	if pcodeRedactionDisabled() {
		fmt.Fprintln(os.Stderr, "WARNING: KEEPRISK_HIDE_PCODE=false —— 敏感输出脱敏已关闭，接口编码/网关配置等将出现在对客输出中。仅限临时调试，切勿在生产开启。")
	}
	cat, err := catalog.Default()
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	kr, err := keeprisk.DefaultClient()
	if err != nil {
		return nil, err
	}
	qm := quota.New(quota.DefaultConfig())
	cc := cache.New(cache.DefaultConfig())
	return &Services{Cat: cat, KR: kr, Quota: qm, Cache: cc}, nil
}
// boolPtr returns a pointer to a bool (for ToolAnnotations *bool fields)
func boolPtr(v bool) *bool { return &v }
