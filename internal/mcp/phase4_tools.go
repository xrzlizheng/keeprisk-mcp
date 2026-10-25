package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── 产业链画像 ───────────────────────────

// chainProfileSection 产业链画像内部调用的 API
type chainProfileSection struct {
	Key      string
	Pcode    string
	Title    string
	ParamKey string // 主体入参键名（chainName / name）
}

// 产业链画像聚合 4 个 API
var chainProfileSections = []chainProfileSection{
	{Key: "chain_analysis", Pcode: "E_017_0081", Title: "产业链数据分析", ParamKey: "chainName"},
	{Key: "region_stats", Pcode: "E_007_1351", Title: "产业链地区统计", ParamKey: "chainName"},
	{Key: "node_stats", Pcode: "E_007_1251", Title: "产业链节点统计", ParamKey: "chainName"},
	{Key: "chain_finance", Pcode: "E_007_1171", Title: "产业链财务数据", ParamKey: "chainHis"},
}

// IndustryChainProfile 产业链画像：并行聚合 4 个产业链分析接口
//
// 设计要点：
//   - 面向产业链招商场景：回答"这条产业链的全貌是什么"
//   - 入参为产业链名称（chain_name），非企业名称
//   - 可选 province/city 缩小范围
func (s *Services) IndustryChainProfile(ctx context.Context, chainName, province, city string) string {
	chainName = strings.TrimSpace(chainName)
	if chainName == "" {
		return mustJSON(map[string]any{"error": "请提供 chain_name（产业链名称，必填）"})
	}

	start := time.Now()
	results := make([]map[string]any, len(chainProfileSections))
	var wg sync.WaitGroup

	for i, sec := range chainProfileSections {
		wg.Add(1)
		go func(idx int, cs chainProfileSection) {
			defer wg.Done()

			params := map[string]any{}
			// 根据 ParamKey 写入主体
			params[cs.ParamKey] = chainName
			// 主体字段镜像：仅在接口确实声明 name 时补。
			//
			// 2026-09-18 修正（飞友科技全量实测）：旧实现无条件补 name，
			// 而内嵌目录中 E_017_0081/E_007_1351 已声明 chainName、并不声明 name，
			// 网关逐条回"未知参数 name，已知字段: chainName, city, county, province…"，
			// 属框架自造噪音（调用方无从行动）。
			declared := declaredParams(s.Cat, cs.Pcode)
			if cs.ParamKey == "chainName" && declared != nil && declared["name"] {
				params["name"] = chainName
			}
			// 附加地区过滤：E_007_1251 的区划参数是 **region 对象** {province,city}，
			// 不是扁平 province（实测 2026-09-17：扁平 province 被上游静默忽略且照常计费，
			// region:{province} 才生效——安徽省节点数从 272074 降至 23179）。
			// 其余产业链接口约定不同：E_007_1131 用 regionList 数组、E_007_1351/E_007_1171 用扁平。
			if cs.Pcode == "E_007_1251" {
				if province != "" || city != "" {
					region := map[string]any{}
					if province != "" {
						region["province"] = province
					}
					if city != "" {
						region["city"] = city
					}
					params["region"] = region
				}
			} else {
				if province != "" {
					params["province"] = province
				}
				if city != "" {
					params["city"] = city
				}
			}

			raw := s.callQuery(ctx, cs.Pcode, params)

			item := map[string]any{
				"key":   cs.Key,
				"pcode": cs.Pcode,
				"title": cs.Title,
			}

			var parsed map[string]any
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr == nil {
				if errStr, ok := parsed["error"].(string); ok && errStr != "" {
					item["status"] = "error"
					item["error"] = errStr
				} else {
					item["status"] = "ok"
					item["data"] = parsed
					dgCP := buildChainProfileDigest(cs.Key, parsed)
					enrichDigestPaging(parsed, dgCP)
					item["digest"] = dgCP
				}
			} else {
				item["status"] = "error"
				item["error"] = "响应解析失败"
			}
			results[idx] = item
		}(i, sec)
	}
	wg.Wait()

	out := map[string]any{
		"chain_name":  chainName,
		"province":    province,
		"city":        city,
		"sections":    results,
		"elapsed_ms":  time.Since(start).Milliseconds(),
		"api_calls":   len(chainProfileSections),
	}

	return mustJSON(out)
}

// buildChainProfileDigest 为产业链画像的每个维度生成结构化摘要
func buildChainProfileDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = count > 0

	switch key {
	case "chain_analysis":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			for _, f := range []string{"chainName", "nodeCount", "enterpriseCount", "totalRevenue", "totalProfit"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}

	case "region_stats":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if regions, ok := data["list"].([]any); ok {
				digest["region_count"] = len(regions)
			}
		}

	case "node_stats":
		digest["node_count"] = count

	case "chain_finance":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			for _, f := range []string{"totalRevenue", "totalProfit", "avgRevenue", "enterpriseCount"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// ─────────────────────────── 计费估算工具 ───────────────────────────

// EstimateBilling 本地计费估算：不调用网关，纯计算
//
// 根据工具名称和参数估算 API 调用次数和费用
// tool_costs 定义每个工具的内部 API 调用次数
func (s *Services) EstimateBilling(tool string, args map[string]any) string {
	// 各工具的内部 API 调用次数
	toolCosts := map[string]int{
		"keeprisk_query":                  1,
		"enterprise_basic_profile":        1,
		"resolve_entity":                  1,
		"enterprise_credit_score":         6,
		"listed_company_profile":          8,
		"supply_chain_relations":          2,
		"investment_readiness_score":      4,
		"control_penetration":             1,
		"actual_controller":               1,
		"batch_query_companies":           0, // 动态计算
		"search_enterprises":              1,
		"search_chain_enterprises":        1,
		"search_chains":                   1,
		"get_chain_nodes":                 1,
		"list_categories":                 0,
		"list_data_capabilities":          0,
		"search_keeprisk_apis":            0,
		"get_keeprisk_api":                0,
		"validate_keeprisk_params":        0,
		"quota_status":                    0,
		"enterprise_extended_profile":     9,
		"listed_company_financials":       13,
		"listed_company_market":           15,
		"annual_report_investments":       4,
		"intellectual_property_portfolio": 5,
		"enterprise_graph_relations":      4,
		"operational_compliance":          4,
		"judicial_deep_dive":              4, // 列表×2 + 反查0/1 + 明细抽样（按记录类型数浮动）
		"data_badge_full":                 4,
		"enterprise_business_detail":      8,
		"enterprise_deep_profile":         6,
		"listed_company_extended":         10,
		"investment_analysis_suite":       7,
		"enterprise_development":          5,
		"investment_event_profile":        5,
		"enterprise_evaluation_report":    5,
		"annual_report_detail":            8,
		"industrial_park_search":          4,
		"industry_chain_profile":          4,
		"fuzzy_search":                    1,
		"company_quick_scan":              0,
		"compare_enterprises":             0,
		"person_risk_profile":             4,
		"entity_risk_timeline":            4,
		"school_alumni_circle":            1,
	}

	// 只读工具（0 次网关调用）
	readOnlyTools := map[string]bool{
		"list_categories":          true,
		"list_data_capabilities":   true,
		"search_keeprisk_apis":     true,
		"get_keeprisk_api":         true,
		"validate_keeprisk_params": true,
		"quota_status":             true,
	}

	out := map[string]any{
		"tool":            tool,
		"is_read_only":    readOnlyTools[tool],
		"estimated_calls": 0,
		"notes":           "",
	}

	if readOnlyTools[tool] {
		out["notes"] = "本地工具，不消耗网关调用配额"
		return mustJSON(out)
	}

	// 尽调快照：按 sections 计算
	if tool == "enterprise_due_diligence_snapshot" {
		sections, _ := args["sections"].(string)
		includeJudicial := true
		if v, ok := args["include_judicial"].(bool); ok {
			includeJudicial = v
		}

		selected, skipped, _ := selectSections(sections, includeJudicial)
		out["estimated_calls"] = len(selected)
		out["sections_selected"] = len(selected)
		out["sections_skipped"] = skipped
		out["notes"] = fmt.Sprintf("每节 = 1 次网关调用；当前选择 %d 节", len(selected))
		return mustJSON(out)
	}

	// 批量查询：单次 API
	if tool == "batch_query_companies" {
		out["estimated_calls"] = 1
		out["notes"] = "单次网关调用"
		return mustJSON(out)
	}

	if tool == "enterprise_evaluation_report" {
		calls := 2
		if _getStr(args, "province") != "" {
			calls = 5
			out["notes"] = "2 个企业维度接口 + 3 个地区维度接口"
		} else {
			out["notes"] = "未提供 province 时仅执行 2 个企业维度接口，地区维度 3 节将跳过"
		}
		out["estimated_calls"] = calls
		return mustJSON(out)
	}

	if tool == "industrial_park_search" {
		calls := 0
		if _getStr(args, "park_id") != "" {
			calls++
		}
		if _getStr(args, "park_name") != "" {
			calls += 3 // 园区列表 + 园区分布 + 园区企业列表
		}
		if _getStr(args, "province") != "" || _getStr(args, "city") != "" {
			calls++
		}
		out["estimated_calls"] = calls
		if calls == 0 {
			out["notes"] = "需提供 park_name、park_id 或 province/city 才会产生调用"
		} else {
			out["notes"] = "按 park_name 约 3 次调用，park_id 直查约 1 次，地区筛选额外 1 次"
		}
		return mustJSON(out)
	}

	if tool == "company_quick_scan" {
		depth, _ := args["depth"].(string)
		dd := 5
		switch strings.ToLower(strings.TrimSpace(depth)) {
		case "light":
			dd = 3
		case "deep":
			dd = 26
		}
		out["estimated_calls"] = 1 + 6 + dd
		out["notes"] = fmt.Sprintf("1 工商 + 6 信用分 + %d 尽调节", dd)
		return mustJSON(out)
	}

	if tool == "compare_enterprises" {
		n := 0
		if v, ok := args["ent_names"].([]any); ok {
			n += len(v)
		}
		if v, ok := args["company_ids"].([]any); ok {
			n += len(v)
		}
		out["estimated_calls"] = n * 10
		out["notes"] = fmt.Sprintf("每家约 10 次 API（6 信用分 + 4 尽调），共 %d 家", n)
		return mustJSON(out)
	}

	// school_alumni_circle：院校入口 = max_pages 页筛选；企业入口 3 次（工商+人员学历+1 页筛选）；
	// 简称输入 +1；include_stats +1
	if tool == "school_alumni_circle" {
		maxPages := 1
		if v, ok := anyToInt(args["max_pages"]); ok && v > 1 {
			maxPages = v
			if maxPages > 10 {
				maxPages = 10
			}
		}
		calls := maxPages
		notes := fmt.Sprintf("%d 次校企筛选（每页 1 次）", maxPages)
		if strings.TrimSpace(_getStr(args, "company_name")) != "" {
			calls = 2 + maxPages
			notes = fmt.Sprintf("2 次（工商基本信息 + 人员学历）+ %d 次校友圈筛选", maxPages)
		} else {
			name := strings.TrimSpace(_getStr(args, "university_name"))
			if name != "" && !looksLikeFullName(name) {
				calls++
				notes += " + 1 次简称解析（传官方全称可省去）"
			}
		}
		if v, ok := args["include_stats"].(bool); ok && v {
			calls++
			notes += " + 1 次校友企业统计"
		}
		out["estimated_calls"] = calls
		out["notes"] = notes
		return mustJSON(out)
	}

	// 其他工具：查表
	if cost, ok := toolCosts[tool]; ok {
		out["estimated_calls"] = cost
		out["notes"] = fmt.Sprintf("内部并行调用 %d 个 API", cost)
	} else {
		out["estimated_calls"] = 1
		out["notes"] = "未知工具，按 1 次调用估算"
	}

	return mustJSON(out)
}

// ─────────────────────────── 企业全景画像 ───────────────────────────

// 企业全景画像聚合 9 个 API：工商 + 人员 + 股东 + 标签 + 联系方式 + 曾用名 + 变更 + 注销 + 对外投资
var extendedProfileSections = []phase6Section{
	{Key: "basic_info", Pcode: "E_007_0501", Title: "工商基本信息", ParamKey: "entName"},
	{Key: "shareholders", Pcode: "E_007_0901", Title: "工商股东", ParamKey: "entName"},
	// key_personnel 的 pageSize 须与其他 E_007_0601 调用点一致（20）：缓存 key 含
	// pageSize，不一致会导致同主体重复计费（2026-09-20 实测一回合 3 次；另有
	// QueryImpl 级分页归一兜底，此处对齐可减少预热期的一次穿透重取）。
	{Key: "key_personnel", Pcode: "E_007_0601", Title: "主要人员", ParamKey: "entName", ExtraParams: map[string]any{"pageSize": 20}},
	{Key: "enterprise_tags", Pcode: "E_007_1021", Title: "企业标签", ParamKey: "entName"},
	{Key: "beneficial_owner", Pcode: "E_007_0301", Title: "最终受益人", ParamKey: "entName"},
	{Key: "actual_controller", Pcode: "E_007_0201", Title: "实际控制权", ParamKey: "entName"},
	{Key: "control_penetration", Pcode: "E_007_0621", Title: "股权穿透", ParamKey: "entName"},
	{Key: "equity_pledge", Pcode: "E_007_2101", Title: "股权出质", ParamKey: "entName"},
	{Key: "data_badges", Pcode: "E_007_1391", Title: "数据角标V2", ParamKey: "companyName"},
}

// EnterpriseExtendedProfile 企业全景画像：并行聚合 9 个工商/股权/标签接口
//
// 设计要点：
//   - 回答"这家企业的全貌是什么"（比 basic_profile 更深入）
//   - 聚合工商基本信息 + 股东 + 人员 + 标签 + UBO + 实控 + 穿透 + 股权出质 + 角标
//   - 每维度含结构化摘要
func (s *Services) EnterpriseExtendedProfile(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, extendedProfileSections, entName, companyID, 10, buildExtendedProfileDigest)
	return buildPhase6Output("enterprise_extended_profile", entity, results, okCount, skippedCount, len(extendedProfileSections), elapsed)
}

// buildExtendedProfileDigest 为企业全景画像的每个维度生成结构化摘要
func buildExtendedProfileDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}

	switch key {
	case "basic_info":
		// 复用尽调的 basic digest 逻辑。
		// 2026-09-18 修正：企业字段嵌在 data 下（信封 {charge,code,data:{entName,...}}），
		// 旧实现直接在顶层取 parsed["entName"] → 恒为空 → has_data=false，
		// 实测飞友科技的工商基本信息节被摘要成"无数据"。
		src := parsed
		if data, ok := parsed["data"].(map[string]any); ok {
			src = data
		}
		entName, _ := src["entName"].(string)
		legal, _ := src["legalName"].(string)
		status, _ := src["entStatus"].(string)
		digest["ent_name"] = entName
		digest["legal_person"] = legal
		digest["status"] = status
		digest["has_data"] = hasAnyData(parsed)

	case "shareholders", "key_personnel":
		count := extractCount(parsed)
		digest["count"] = count
		digest["has_data"] = count > 0
		digest["summary"] = fmt.Sprintf("%s %d 人", key, count)

	case "enterprise_tags":
		tags := parseTags(parsed)
		digest["tags"] = tags
		digest["tag_count"] = len(tags)
		digest["has_data"] = len(tags) > 0

	case "beneficial_owner":
		count := extractCount(parsed)
		names := extractBeneficialNames(parsed)
		digest["count"] = count
		digest["has_data"] = count > 0
		if names != "" {
			digest["ubo_names"] = names
		}

	case "actual_controller":
		// 字段名以实测响应为准（2026-09-18）：E_007_0201 返回 {total, code, message, items[]}，
		// 并没有 controllerName/controlChain 两个字段——旧实现找它们恒不命中，
		// has_data 落到 `len(digest) > 1` 因只有 1 个键而判 false，
		// 实测飞友科技 5 条实际控制权记录被摘要成"无数据"。
		// 文档依据：接口文档 data 节点说明为 `total 总条数` / `items 集合` / `chainList 控制人 path`。
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if items, ok := data["items"].([]any); ok {
				digest["controller_count"] = len(items)
				var names []string
				for _, it := range items {
					if m, ok := it.(map[string]any); ok {
						if n, ok := m["name"].(string); ok && n != "" {
							names = append(names, n)
						}
					}
				}
				if len(names) > 0 {
					digest["controlled_sample"] = names
				}
			}
		}
		digest["has_data"] = hasAnyData(parsed)
		// 语义提示：文档示例为「百度网讯 → 被投企业」，即向下控制，不是上游实控人
		digest["direction"] = "outbound（被控企业清单；上游实控人请用 beneficial_owner）"

	case "control_penetration":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if investors, ok := data["investorList"].([]any); ok {
				digest["outbound_count"] = len(investors)
			}
			if holders, ok := data["holderList"].([]any); ok {
				digest["inbound_count"] = len(holders)
			}
		}
		digest["has_data"] = hasAnyData(parsed)

	case "equity_pledge":
		count := extractCount(parsed)
		digest["count"] = count
		digest["has_data"] = hasAnyData(parsed)
		if count > 0 {
			digest["risk_level"] = "yellow"
			digest["summary"] = fmt.Sprintf("股权出质 %d 条", count)
		}

	case "data_badges":
		// E_007_1391 返回按业务域分组的扁平计数字典（bussiness/intellectual/judicature...），
		// 既无数组也无 total。旧实现的 has_data 依据（len(digest)>0）恒真，属巧合正确；
		// 现改为统一证据判定，并用支持嵌套的解析器取计数。
		digest["badges"] = parseNestedBadges(parsed)
		digest["has_data"] = hasAnyData(parsed)

	default:
		count := extractCount(parsed)
		digest["count"] = count
		digest["has_data"] = hasAnyData(parsed)
		if total, ok := extractTotal(parsed); ok {
			digest["total"] = total
		}
	}

	return digest
}
