package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── 上市公司画像维度定义 ───────────────────────────

// 上市公司画像内部聚合的 8 个核心 API（PDF 主体多为 companyName；定量评级为 name）
//
// number=返回报告期数：上游 number="0" 表示**全部报告期**（科大讯飞实测 77 期 × 每期两组股东
// = 342KB ≈ 13 万 token，单次调用可撑爆模型上下文）。默认 "1"（最新一期）；
// 需要历史多期时用 keeprisk_query 直调该接口并显式传 number。
var listedSections = []phase6Section{
	{Key: "overview", Pcode: "E_010_0401", Title: "上市公司概况", ParamKey: "companyName", ExtraParams: map[string]any{"type": "10015"}},
	{Key: "key_metrics", Pcode: "E_010_0241", Title: "关键指标数据", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "stock_quote", Pcode: "E_010_0501", Title: "股票行情", ParamKey: "companyName"},
	{Key: "top10_shareholders", Pcode: "E_010_0601", Title: "十大股东", ParamKey: "companyName", ExtraParams: map[string]any{"number": "1", "type": "10018"}},
	{Key: "top10_circulating", Pcode: "E_010_0541", Title: "十大流通股东", ParamKey: "companyName", ExtraParams: map[string]any{"number": "1"}},
	{Key: "capital_structure", Pcode: "E_010_0321", Title: "股本结构", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "main_business", Pcode: "E_010_0571", Title: "主营业务", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "quant_rating", Pcode: "E_010_1201", Title: "定量评级", ParamKey: "name"},
}

// ─────────────────────────── 核心实现 ───────────────────────────

// ListedCompanyProfile 上市公司画像：并行聚合 8 个核心上市信息接口
//
// 设计要点：
//   - 覆盖概况、关键指标、股票行情、股东结构、主营业务、定量评级等核心维度
//   - 并行调用，单接口失败不影响其他维度
//   - 输出包含各维度的结构化摘要
//   - 复用通用 runPhase6Aggregate 引擎，避免重复 WaitGroup 模板代码
func (s *Services) ListedCompanyProfile(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, listedSections, entName, companyID, 5, buildListedDigest)

	out := map[string]any{
		"entity":      entity,
		"sections":    results,
		"elapsed_ms":  elapsed,
		"api_calls":   len(listedSections) - skippedCount,
		"total_count": len(listedSections),
		"ok_count":    okCount,
		"error_count": len(listedSections) - okCount - skippedCount,
		"_agent_meta": agentMeta(entity),
	}
	if skippedCount > 0 {
		out["skipped_count"] = skippedCount
	}
	return mustJSON(out)
}

// buildListedDigest 为上市公司画像的每个维度生成结构化摘要
func buildListedDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	} else {
		data = parsed
	}

	switch key {
	case "overview":
		// 提取公司概况核心字段
		for _, field := range []string{"secucode", "secuabbr", "secumarket", "listedDate", "orgNameCn", "orgShortName", "industry", "mainBusiness"} {
			if v, ok := data[field]; ok {
				digest[field] = v
			}
		}
		digest["has_data"] = len(digest) > 0

	case "key_metrics":
		// 关键指标：提取最近年份数据
		count := extractCount(parsed)
		digest["record_count"] = count
		digest["has_data"] = count > 0

	case "stock_quote":
		// 股票行情
		for _, field := range []string{"price", "change", "changePct", "volume", "turnover", "secucode", "secuabbr"} {
			if v, ok := data[field]; ok {
				digest[field] = v
			}
		}
		digest["has_data"] = len(digest) > 0

	case "top10_shareholders", "top10_circulating":
		// 载荷按报告期分组：bq[i]={enddate, tc:[], zc:[]}。实测形态（科大讯飞 2026-09-20）：
		// envelope.data = {data:{bq,resultdate}, msg, status} —— 即 bq 在本地 data 变量的
		// data["data"]["bq"]（两跳）；另兼容 bq 直接挂在 data 下的形态。
		// extractCount 认不出该形态 → 恒 shareholder_count:0 / has_data:false，
		// 而原始载荷可达数百 KB（全部报告期），摘要与数据完全矛盾。
		bqHolder := data
		if _, has := data["bq"]; !has {
			if d2, ok := data["data"].(map[string]any); ok {
				bqHolder = d2
			}
		}
		if periods, ok := bqHolder["bq"].([]any); ok && len(periods) > 0 {
			digest["period_count"] = len(periods)
			if first, ok := periods[0].(map[string]any); ok {
				zc, _ := first["zc"].([]any)
				tc, _ := first["tc"].([]any)
				digest["shareholder_count"] = len(zc) + len(tc)
				if ed, ok := first["enddate"].(string); ok && ed != "" {
					digest["latest_period"] = ed
				}
			}
		}
		sc, hasSC := anyToInt(digest["shareholder_count"])
		pc, _ := anyToInt(digest["period_count"])
		if !hasSC {
			sc = extractCount(parsed)
			digest["shareholder_count"] = sc
		}
		digest["has_data"] = sc > 0 || pc > 0

	case "capital_structure":
		count := extractCount(parsed)
		digest["record_count"] = count
		digest["has_data"] = count > 0

	case "main_business":
		// 主营业务
		for _, field := range []string{"mainOperBusiness", "mainOperIncome", "classifyByIndustry", "classifyByProduct", "classifyByRegion"} {
			if v, ok := data[field]; ok {
				digest[field] = v
			}
		}
		digest["has_data"] = len(digest) > 0

	case "quant_rating":
		// 定量评级
		for _, field := range []string{"rating", "score", "rank", "grade"} {
			if v, ok := data[field]; ok {
				digest[field] = v
			}
		}
		digest["has_data"] = len(digest) > 0

	default:
		count := extractCount(parsed)
		digest["record_count"] = count
		digest["has_data"] = count > 0
	}

	return digest
}

// ─────────────────────────── 供应链关系工具 ───────────────────────────

// 供应链关系工具内部调用的 API（复用通用 phase6Section 类型）。
// 注意：E_007_1251 产业链节点统计的主体是 chainName（产业链名称），企业维度语义
// 无法满足（曾误传企业名导致整节 error），已跳过；如需节点统计请走
// industry_chain_profile（chain_name 维度）。
var supplyChainSections = []phase6Section{
	{Key: "suppliers_customers", Pcode: "E_011_0391", Title: "供应商与客户", ParamKey: "companyName"},
	{Key: "industry_chain_map", Pcode: "E_005_0091", Title: "企业产业链图谱", ParamKey: "companyName"},
	{Key: "industry_chain_nodes", Pcode: "E_007_1251", Title: "产业链节点统计", ParamKey: "name", SkipReason: "requires_chain_name_not_entity"},
}

// SupplyChainRelations 供应链关系：聚合供应商/客户 + 产业链图谱 + 节点统计
//
// 设计要点：
//   - 回答"这家企业的上下游是谁"（供应链风控/招商核心问题）
//   - 并行调用 3 个接口，单接口失败不影响其他
//   - 复用通用 runPhase6Aggregate 引擎，避免重复 WaitGroup 模板代码
func (s *Services) SupplyChainRelations(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, supplyChainSections, entName, companyID, 10, buildSupplyChainDigest)

	out := map[string]any{
		"entity":      entity,
		"sections":    results,
		"elapsed_ms":  elapsed,
		"api_calls":   len(supplyChainSections) - skippedCount,
		"total_count": len(supplyChainSections),
		"ok_count":    okCount,
		"error_count": len(supplyChainSections) - okCount - skippedCount,
		"_agent_meta": agentMeta(entity),
	}
	if skippedCount > 0 {
		out["skipped_count"] = skippedCount
	}
	return mustJSON(out)
}

// buildSupplyChainDigest 为供应链关系的每个维度生成结构化摘要
func buildSupplyChainDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = hasAnyData(parsed)

	switch key {
	case "suppliers_customers":
		// 尝试提取供应商/客户名称列表
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if suppliers, ok := data["suppliers"].([]any); ok {
				digest["supplier_count"] = len(suppliers)
			}
			if customers, ok := data["customers"].([]any); ok {
				digest["customer_count"] = len(customers)
			}
		}

	case "industry_chain_map":
		// 产业链图谱：提取产业链名称和节点数
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if chainName, ok := data["chainName"].(string); ok {
				digest["chain_name"] = chainName
			}
			if nodes, ok := data["nodes"].([]any); ok {
				digest["node_count"] = len(nodes)
			}
		}

	case "industry_chain_nodes":
		// 节点统计
		digest["node_count"] = count
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// ─────────────────────────── 投资准备度评分 ───────────────────────────

// investmentSection 投资准备度评分工具内部调用的 API
type investmentSection struct {
	Key      string
	Pcode    string
	Title    string
	ParamKey string
}

// 投资准备度评分聚合 4 个 API：投资意愿评分 + 投资意愿分析 + 信用评分 + 综合评分
var investmentSections = []investmentSection{
	{Key: "willingness_score", Pcode: "E_017_0161", Title: "投资意愿评分", ParamKey: "companyName"},
	{Key: "willingness_analysis", Pcode: "E_017_0231", Title: "投资意愿分析", ParamKey: "companyName"},
	{Key: "comprehensive_score", Pcode: "E_017_0121", Title: "企业综合评分", ParamKey: "companyName"},
	{Key: "data_badges", Pcode: "E_007_1391", Title: "数据角标V2", ParamKey: "companyName"},
}

// InvestmentReadinessScore 投资准备度评分：聚合投资意愿 + 综合评分 + 数据角标
//
// 设计要点：
//   - 面向招商场景：回答"这家企业值不值得招"
//   - 聚合投资意愿评分/分析、企业综合评分、数据角标
//   - 输出结构化评估：投资意愿等级 + 综合评分 + 风险信号
//
// 2026-09-20 修正：E_017_0161/E_017_0231（投资意愿评分/分析）按接口文档为
// **产业链维度接口**——必填 chainName（产业链名称），regionMap{province,city}
// 也是文档示例的一部分。旧实现只传 companyName，两节对**所有主体恒 101 参数错误**
// （实测智侒信/科大讯飞均如此；补全 chainName+regionMap 后返回正常 110/100）。
// 现规则：未提供 chain_name 时两节直接 skipped（0 网关调用）；提供则补全参数。
func (s *Services) InvestmentReadinessScore(ctx context.Context, entName, companyID, chainName, province, city string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	chainName = strings.TrimSpace(chainName)
	province = strings.TrimSpace(province)
	city = strings.TrimSpace(city)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	start := time.Now()
	results := make([]map[string]any, len(investmentSections))
	var wg sync.WaitGroup

	for i, sec := range investmentSections {
		// 投资意愿两节是产业链维度接口：无 chain_name 时确定失败，直接跳过省 2 次网关调用
		if (sec.Key == "willingness_score" || sec.Key == "willingness_analysis") && chainName == "" {
			results[i] = map[string]any{
				"key":    sec.Key,
				"pcode":  sec.Pcode,
				"title":  sec.Title,
				"status": "skipped",
				"reason": "投资意愿评分/分析为产业链维度接口（文档必填 chainName，示例还含 regionMap）：请传 chain_name（如「人工智能产业链」，可用 search_chains 查有效名称）及可选 province/city 后重查",
			}
			continue
		}
		wg.Add(1)
		go func(idx int, is investmentSection) {
			defer wg.Done()

			params := map[string]any{}
			if entName != "" {
				params[is.ParamKey] = entName
			}
			if companyID != "" {
				params["companyId"] = companyID
			}
			if is.Key == "willingness_score" || is.Key == "willingness_analysis" {
				params["chainName"] = chainName
				if province != "" || city != "" {
					region := map[string]any{}
					if province != "" {
						region["province"] = province
					}
					if city != "" {
						region["city"] = city
					}
					params["regionMap"] = region
				}
			}

			raw := s.callQuery(ctx, is.Pcode, params)

			item := map[string]any{
				"key":   is.Key,
				"pcode": is.Pcode,
				"title": is.Title,
			}

			var parsed map[string]any
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr == nil {
				if errStr, ok := parsed["error"].(string); ok && errStr != "" {
					item["status"] = "error"
					// 透出可行动的错误细节：错误类别 + 网关 code/message。
					// 旧实现只放类别词（"api"），模型无从判断是参数问题还是未订阅。
					item["error"] = gatewayErrText(raw)
					if code, ok := parsed["code"]; ok {
						item["code"] = code
					}
				} else {
					item["status"] = "ok"
					item["data"] = parsed
				}
			} else {
				item["status"] = "error"
				item["error"] = "响应解析失败"
			}
			results[idx] = item
		}(i, sec)
	}
	wg.Wait()

	// 汇总为结构化投资准备度评估
	assessment := buildInvestmentAssessment(entName, results)
	assessment["elapsed_ms"] = time.Since(start).Milliseconds()
	executed := 0
	for _, r := range results {
		if r["status"] != "skipped" {
			executed++
		}
	}
	assessment["api_calls"] = executed

	return mustJSON(assessment)
}

// buildInvestmentAssessment 将投资准备度 API 响应汇总为结构化评估
func buildInvestmentAssessment(entName string, results []map[string]any) map[string]any {
	out := map[string]any{
		"entity": entName,
	}

	var errors []string
	sections := make([]map[string]any, 0, len(results))

	for _, item := range results {
		key, _ := item["key"].(string)
		status, _ := item["status"].(string)

		section := map[string]any{
			"key":    key,
			"title":  item["title"],
			"status": status,
		}

		if status != "ok" {
			errors = append(errors, fmt.Sprintf("%s: %v", key, item["error"]))
			sections = append(sections, section)
			continue
		}

		if data, ok := item["data"].(map[string]any); ok {
			section["data"] = data

			// 为每个维度提取摘要（字段名以各接口文档为准，2026-09-18 逐份核对原文）
			switch key {
			case "willingness_score":
				// E_017_0161 文档：`finalScore 投资意愿综合评分`。
				// 旧代码读 `score`——文档中不存在该字段，摘要恒缺失。
				if d, ok := data["data"].(map[string]any); ok {
					if fs, ok := d["finalScore"]; ok {
						section["score"] = fs
					}
					if rec, ok := d["recommendations"]; ok {
						section["recommendations"] = rec
					}
				}
			case "willingness_analysis":
				// E_017_0231 文档：该接口是**异步任务**——返回 task_id/message/status_url，
				// 字段说明为「task_id 任务id，判断该字段有值则成功」。
				// 旧代码读 `rating`——文档中不存在该字段，恒缺失。
				if d, ok := data["data"].(map[string]any); ok {
					for _, k := range []string{"task_id", "message", "status_url", "entity_name"} {
						if v, ok := d[k]; ok {
							section[k] = v
						}
					}
					if _, ok := d["task_id"]; ok {
						section["async_task"] = true
						section["async_note"] = "投资意愿分析为异步任务：凭 task_id 轮询 status_url 获取结果"
					}
				}
			case "comprehensive_score":
				// E_017_0121 文档：`entEntireScore 企业综合评分`。
				// 旧代码读 `score`——文档中不存在该字段，摘要恒缺失。
				// 标度自适应复用 parseScoreDimension（文档示例 99.99 为 0–100，
				// 实测另有 852.88 为 0–1000，见 credit_score.go 的说明）。
				if d, ok := data["data"].(map[string]any); ok {
					dim := parseScoreDimension("comprehensive_score", "企业综合评分",
						map[string]any{"data": d})
					if dim.HasData {
						section["score"] = dim.Score
						if dim.ScoreScale != "" {
							section["score_scale_note"] = dim.ScoreScale
						}
					}
				}
			case "data_badges":
				section["badges"] = parseBadges(data)
			}
		}
		sections = append(sections, section)
	}

	out["sections"] = sections
	if len(errors) > 0 {
		out["errors"] = errors
	}

	return out
}
