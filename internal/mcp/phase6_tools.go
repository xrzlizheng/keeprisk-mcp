package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// ═══════════════════════════ Phase 6 聚合工具 ═══════════════════════════
// 8 个新聚合工具，覆盖约 53 个额外 API，将覆盖率从 ~45% 提升至 ~68%

// ─────────────────────────── 通用并行聚合框架 ───────────────────────────

// phase6Section 通用聚合维度定义
type phase6Section struct {
	Key         string
	Pcode       string
	Title       string
	ParamKey    string
	ExtraParams map[string]any // 可选：PDF 必填的非主体字段（如 type、number、regLocation）
	SkipReason  string         // 非空则跳过网关调用（缺前置字段等）
}

// declaredParams 返回该 pcode 在内嵌目录中声明的业务字段名集合。
// 目录缺失该 pcode（或未声明任何字段）时返回 nil，调用方据此退化为"全部注入"。
func declaredParams(cat *catalog.Catalog, pcode string) map[string]bool {
	if cat == nil {
		return nil
	}
	entry := cat.GetByPcode(pcode)
	if entry == nil {
		return nil
	}
	out := make(map[string]bool, len(entry.BusinessParams))
	for _, p := range entry.BusinessParams {
		if n := strings.TrimSpace(p.Name); n != "" {
			out[n] = true
		}
	}
	if props, ok := entry.InputSchema["properties"].(map[string]any); ok {
		for k := range props {
			out[strings.TrimSpace(k)] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// runPhase6Aggregate 通用并行聚合执行器
//
// ParamKey=="companyId" 的节仅在显式传入 company_id 时执行：
// 企业名称绝不能误填进 companyId 字段（网关必然查无数据），未传 company_id 时标记 skipped。
func runPhase6Aggregate(
	svc *Services,
	ctx context.Context,
	sections []phase6Section,
	entName, companyID string,
	pageSize int,
	digestFn func(key string, parsed map[string]any) map[string]any,
	maxConcurrency ...int,
) (string, []map[string]any, int, int, int64) {
	start := time.Now()
	results := make([]map[string]any, len(sections))
	var wg sync.WaitGroup

	// 并发限制，对齐 Python run_aggregate 的 max_concurrency=6，防止打爆网关连接池。
	// 变长参数实现向后兼容：现有调用方无需修改，新调用方可显式传入。
	mc := 6
	if len(maxConcurrency) > 0 && maxConcurrency[0] > 0 {
		mc = maxConcurrency[0]
	}
	sem := make(chan struct{}, mc)

	for i, sec := range sections {
		if sec.SkipReason != "" {
			results[i] = map[string]any{
				"key":    sec.Key,
				"pcode":  sec.Pcode,
				"title":  sec.Title,
				"status": "skipped",
				"reason": sec.SkipReason,
			}
			continue
		}
		if sec.ParamKey == "companyId" && companyID == "" {
			results[i] = map[string]any{
				"key":    sec.Key,
				"pcode":  sec.Pcode,
				"title":  sec.Title,
				"status": "skipped",
				"reason": "missing_company_id",
			}
			continue
		}
		wg.Add(1)
		go func(idx int, s phase6Section) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			declared := declaredParams(svc.Cat, s.Pcode)

			params := map[string]any{}
			// 仅在接口声明了分页参数时才注入。
			//
			// 2026-09-18 修正（飞友科技全量实测）：旧实现无条件注入 pageNo/pageSize，
			// 而多数接口并不声明它们 → 网关逐条回 "未知参数 pageNo，已知字段: ..."，
			// 聚合工具把这些 warning 原样透传给调用方，实测 102 处、25 条去重消息，
			// 全部是框架自己造的噪音，对 Agent 完全不可行动。
			// 同一改动同时修复缓存碎片：注入的额外参数会进 cache key，
			// 使"同一主体同一接口"经不同工具调用时命中不到彼此（实测 E_007_0501
			// 间隔 1 秒被两个工具各调一次、两次都真实计费）。
			if declared == nil || declared["pageNo"] {
				params["pageNo"] = 1
			}
			if declared == nil || declared["pageSize"] {
				params["pageSize"] = pageSize
			}
			if s.ParamKey == "companyId" {
				params["companyId"] = companyID
			} else if s.ParamKey != "" && s.ParamKey != "_" {
				if entName != "" {
					params[s.ParamKey] = entName
					// catalog 常误标 required_one_of=name；PDF 主体为 entName/companyName。
					// 双向镜像仅在**目标字段确实被该接口声明**时进行，否则网关会把
					// 镜像出来的字段当成未知参数（实测"未知参数 name"即此来源）。
					switch s.ParamKey {
					case "entName", "companyName":
						if declared == nil || declared["name"] {
							params["name"] = entName
						}
					case "name":
						if declared == nil || declared["entName"] {
							params["entName"] = entName
						}
					}
				}
				if companyID != "" && (declared == nil || declared["companyId"]) {
					// 仅在接口声明 companyId 时注入；实测聚合器曾无条件注入，
					// 网关回"未知参数 companyId，已知字段: companyName, number, type…"
					// （2026-09-18 实测于 listed_company_market.block_trades / E_010_0531）
					params["companyId"] = companyID
				}
			}
			for k, v := range s.ExtraParams {
				params[k] = v
			}
			raw := svc.callQuery(ctx, s.Pcode, params)
			item := map[string]any{"key": s.Key, "pcode": s.Pcode, "title": s.Title}
			var parsed map[string]any
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr == nil {
				if errStr, ok := parsed["error"].(string); ok && errStr != "" {
					item["status"] = "error"
					// QueryImpl 错误形态 {"error":"api","code":101,"message":"…"}：
					// 只透传 "api" 会让模型无从判断参数错误/服务故障，message 必须透出
					item["error_type"] = errStr
					if m, ok := parsed["message"].(string); ok && m != "" {
						item["error"] = m
					} else {
						item["error"] = errStr
					}
					if c, ok := parsed["code"]; ok {
						item["gateway_code"] = c
					}
				} else {
					item["status"] = "ok"
					item["data"] = parsed
					if digestFn != nil {
						dg := digestFn(s.Key, parsed)
						// 统一补齐分页口径（total / page_count），保证各聚合工具一致，
						// 避免逐个 digest 构造器遗漏（实测知产组合 5 节曾漏）。
						enrichDigestPaging(parsed, dg)
						item["digest"] = dg
					}
				}
			} else {
				item["status"] = "error"
				item["error"] = "响应解析失败"
			}
			results[idx] = item
		}(i, sec)
	}
	wg.Wait()

	okCount, skippedCount := 0, 0
	for _, r := range results {
		switch r["status"] {
		case "ok":
			okCount++
		case "skipped":
			skippedCount++
		}
	}
	return entName, results, okCount, skippedCount, time.Since(start).Milliseconds()
}

// buildPhase6Output 构建通用聚合输出（统一附 _agent_meta 反幻觉元数据）
func buildPhase6Output(toolName string, entity string, results []map[string]any, okCount, skippedCount, total int, elapsedMs int64) string {
	out := map[string]any{
		"entity":      entity,
		"sections":    results,
		"elapsed_ms":  elapsedMs,
		"api_calls":   total - skippedCount,
		"total_count": total,
		"ok_count":    okCount,
		"error_count": total - okCount - skippedCount,
		"_agent_meta": agentMeta(entity),
	}
	if skippedCount > 0 {
		out["skipped_count"] = skippedCount
	}
	return mustJSON(out)
}

// ═══════════════════════════ 1. 全量数据角标 ═══════════════════════════

var dataBadgeFullSections = []phase6Section{
	// 2026-09-18 修正（飞友科技全量实测）：司法角标 E_003_0331 的主体字段是 entName。
	// 原 ParamKey 写 "name"，网关逐次回"未知参数 name，已知字段: entName, type…"，
	// 属框架传错字段（其余三个角标节均为 entName，此处是唯一的例外）。
	{Key: "judicial_badge", Pcode: "E_003_0331", Title: "数据角标-司法信息", ParamKey: "entName"},
	{Key: "enterprise_badge", Pcode: "E_007_0871", Title: "数据角标-企业信息", ParamKey: "entName"},
	{Key: "business_badge", Pcode: "E_011_0171", Title: "数据角标-经营信息", ParamKey: "entName"},
	{Key: "ip_badge", Pcode: "E_012_0061", Title: "数据角标-知识产权", ParamKey: "entName"},
}

// DataBadgeFull 全量数据角标：并行聚合 4 个角标接口
func (s *Services) DataBadgeFull(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, dataBadgeFullSections, entName, companyID, 5, buildGenericCountDigest)

	// 构建角标汇总
	summary := map[string]any{}
	totalRecords := 0
	for _, r := range results {
		if r["status"] != "ok" {
			continue
		}
		key, _ := r["key"].(string)
		digest, _ := r["digest"].(map[string]any)
		count, _ := digest["record_count"].(int)
		totalRecords += count
		summary[key] = count
	}
	summary["total_badge_count"] = totalRecords
	summary["has_badge"] = totalRecords > 0

	out := map[string]any{
		"entity":      entity,
		"sections":    results,
		"badge_summary": summary,
		"elapsed_ms":  elapsed,
		"api_calls":   len(dataBadgeFullSections) - skippedCount,
		"total_count": len(dataBadgeFullSections),
		"ok_count":    okCount,
		"error_count": len(dataBadgeFullSections) - okCount - skippedCount,
	}
	if skippedCount > 0 {
		out["skipped_count"] = skippedCount
	}
	return mustJSON(out)
}

// ═══════════════════════════ 2. 企业经营详情 ═══════════════════════════

var businessDetailSections = []phase6Section{
	{Key: "branches", Pcode: "E_007_0701", Title: "分支机构", ParamKey: "entName"},
	{Key: "changes", Pcode: "E_007_0801", Title: "变更信息", ParamKey: "entName"},
	{Key: "bidding", Pcode: "E_007_1341", Title: "标讯企业", ParamKey: "entName"},
	{Key: "admin_license_credit", Pcode: "E_007_4901", Title: "行政许可_信用中国", ParamKey: "entName"},
	{Key: "ip_pledge", Pcode: "E_007_3701", Title: "知识产权出质登记", ParamKey: "entName"},
	{Key: "admin_penalty_credit", Pcode: "E_007_5001", Title: "行政处罚_信用中国", ParamKey: "entName"},
	{Key: "actual_shareholders", Pcode: "E_007_1371", Title: "实际股东", ParamKey: "entName"},
	{Key: "cancellation", Pcode: "E_007_2201", Title: "注销信息", ParamKey: "entName"},
}

// EnterpriseBusinessDetail 企业经营详情：并行聚合 8 个经营/合规接口
func (s *Services) EnterpriseBusinessDetail(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, businessDetailSections, entName, companyID, 10, buildGenericCountDigest)
	return buildPhase6Output("enterprise_business_detail", entity, results, okCount, skippedCount, len(businessDetailSections), elapsed)
}

// ═══════════════════════════ 3. 企业深度画像补充 ═══════════════════════════

var deepProfileSections = []phase6Section{
	// 董监高节（E_007_0102）已于 1cc0574 决策移除（人员数据统一走 person_risk_profile），
	// e654198 重构搬表时被误带回，2026-09-22 review 时按当时意图补删。
	{Key: "registration_verify", Pcode: "E_007_0641", Title: "企业注册地核验", ParamKey: "entName"},
	{Key: "coordinates", Pcode: "E_007_0961", Title: "企业坐标", ParamKey: "entName"},
	{Key: "former_names", Pcode: "E_007_4101", Title: "公司曾用名", ParamKey: "entName"},
	{Key: "contact_info", Pcode: "E_007_4201", Title: "公司联系方式", ParamKey: "entName"},
	{Key: "contact_v2", Pcode: "E_007_1381", Title: "联系方式V2", ParamKey: "companyName"},
	{Key: "related_info", Pcode: "E_017_0201", Title: "企业关联信息查询", ParamKey: "entName"},
}

// EnterpriseDeepProfile 企业深度画像补充：并行聚合 7 个工商附加接口。
// 注册地核验（E_007_0641）PDF 要求 entName+regLocation+creditCode，先查工商照面补齐后调用。
func (s *Services) EnterpriseDeepProfile(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	sections := make([]phase6Section, len(deepProfileSections))
	copy(sections, deepProfileSections)
	regLocation, creditCode := s.lookupRegFields(ctx, entName, companyID)
	for i := range sections {
		if sections[i].Key != "registration_verify" {
			continue
		}
		if regLocation != "" && creditCode != "" {
			sections[i].ExtraParams = map[string]any{
				"regLocation": regLocation,
				"creditCode":  creditCode,
			}
		} else {
			sections[i].SkipReason = "missing_reg_location_or_credit_code"
		}
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, sections, entName, companyID, 10, buildGenericCountDigest)
	return buildPhase6Output("enterprise_deep_profile", entity, results, okCount, skippedCount, len(sections), elapsed)
}

// lookupRegFields 从工商基本信息提取注册地址与统一社会信用代码，供 E_007_0641 使用。
func (s *Services) lookupRegFields(ctx context.Context, entName, companyID string) (regLocation, creditCode string) {
	params := map[string]any{}
	if entName != "" {
		params["entName"] = entName
		params["name"] = entName
	}
	if companyID != "" {
		params["companyId"] = companyID
	}
	raw := s.callQuery(ctx, "E_007_0501", params)
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return "", ""
	}
	if errStr, ok := parsed["error"].(string); ok && errStr != "" {
		return "", ""
	}
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		return "", ""
	}
	for _, k := range []string{"regLocation", "regAddr", "address"} {
		if v, ok := data[k].(string); ok && strings.TrimSpace(v) != "" {
			regLocation = strings.TrimSpace(v)
			break
		}
	}
	for _, k := range []string{"creditCode", "taxNo"} {
		if v, ok := data[k].(string); ok && strings.TrimSpace(v) != "" {
			creditCode = strings.TrimSpace(v)
			break
		}
	}
	return regLocation, creditCode
}

// ═══════════════════════════ 4. 上市公司扩展信息 ═══════════════════════════

var listedExtendedSections = []phase6Section{
	{Key: "cbirc_penalty", Pcode: "E_010_0171", Title: "银保监行政处罚", ParamKey: "entName", ExtraParams: map[string]any{"type": "1_2_3_4_5_6"}},
	{Key: "executive_other", Pcode: "E_010_0261", Title: "高管其他数据", ParamKey: "companyName"},
	{Key: "supervisory_board", Pcode: "E_010_0291", Title: "高管监事会数据", ParamKey: "companyName"},
	{Key: "board_of_directors", Pcode: "E_010_0311", Title: "高管董事会数据", ParamKey: "companyName"},
	{Key: "industry_comparison", Pcode: "E_010_0361", Title: "行业市场比较", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "industry_analysis", Pcode: "E_010_0371", Title: "行业分析", ParamKey: "companyName"},
	{Key: "industry_financials", Pcode: "E_010_0391", Title: "行业财务数据", ParamKey: "companyName"},
	{Key: "dragon_tiger_detail", Pcode: "E_010_0491", Title: "龙虎榜明细V2", ParamKey: "companyName"},
	{Key: "a_share_info", Pcode: "E_010_1101", Title: "A股发行信息", ParamKey: "companyName"},
	{Key: "industry_code", Pcode: "E_010_0801", Title: "行业代码", ParamKey: "_", SkipReason: "missing_companycode"},
}

// ListedCompanyExtended 上市公司扩展信息：并行聚合 10 个上市扩展接口
func (s *Services) ListedCompanyExtended(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, listedExtendedSections, entName, companyID, 5, buildGenericCountDigest)
	return buildPhase6Output("listed_company_extended", entity, results, okCount, skippedCount, len(listedExtendedSections), elapsed)
}

// ═══════════════════════════ 5. 招商宝分析套件 ═══════════════════════════

var investmentAnalysisSections = []phase6Section{
	// E_017_0131 的 type 为实测必填（目录 required_one_of 漏标）：只传 companyName 恒 101；
	// {companyName, type:1} 实测成功（2026-09-20 智侒信直调验证，返回城市招聘分布）
	{Key: "recruitment_analysis", Pcode: "E_017_0131", Title: "招聘信息分析", ParamKey: "companyName", ExtraParams: map[string]any{"type": 1}},
	{Key: "patent_analysis", Pcode: "E_017_0141", Title: "专利信息分析", ParamKey: "companyName"},
	{Key: "batch_count", Pcode: "E_017_0151", Title: "批量查询数量", ParamKey: "companyName"},
	{Key: "financial_health", Pcode: "E_017_0171", Title: "财务健康分析", ParamKey: "companyName"},
	{Key: "investment_behavior", Pcode: "E_017_0181", Title: "投资行为分析", ParamKey: "companyName"},
	// E_017_0211 文档实为「购地信息分析」（参数含 committedTimeMap 约定交地时间；
	// 目录/文档名一致）。此前误标"股东信息分析"——无该名称的接口，属错标。
	{Key: "land_purchase_analysis", Pcode: "E_017_0211", Title: "购地信息分析", ParamKey: "companyId"},
	{Key: "rd_strength", Pcode: "E_017_0221", Title: "研发实力分析", ParamKey: "companyName"},
}

// InvestmentAnalysisSuite 招商宝分析套件：并行聚合 7 个招商分析接口
func (s *Services) InvestmentAnalysisSuite(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, investmentAnalysisSections, entName, companyID, 5, buildGenericCountDigest)

	// 构建分析汇总
	summary := map[string]any{}
	for _, r := range results {
		if r["status"] != "ok" {
			continue
		}
		key, _ := r["key"].(string)
		digest, _ := r["digest"].(map[string]any)
		count, _ := digest["record_count"].(int)
		summary[key] = count
	}

	out := map[string]any{
		"entity":         entity,
		"sections":       results,
		"analysis_summary": summary,
		"elapsed_ms":     elapsed,
		"api_calls":      len(investmentAnalysisSections) - skippedCount,
		"total_count":    len(investmentAnalysisSections),
		"ok_count":       okCount,
		"error_count":    len(investmentAnalysisSections) - okCount - skippedCount,
	}
	if skippedCount > 0 {
		out["skipped_count"] = skippedCount
	}
	return mustJSON(out)
}

// ═══════════════════════════ 6. 企业发展 ═══════════════════════════

var developmentSections = []phase6Section{
	{Key: "core_team", Pcode: "E_018_0011", Title: "核心团队", ParamKey: "name"},
	{Key: "financing_history", Pcode: "E_018_0021", Title: "融资历史", ParamKey: "name"},
	{Key: "competitors", Pcode: "E_018_0031", Title: "竞品信息", ParamKey: "name"},
	{Key: "business_info", Pcode: "E_018_0041", Title: "企业业务", ParamKey: "name"},
	{Key: "development_history", Pcode: "E_018_0171", Title: "企业发展历程", ParamKey: "companyId"},
}

// buildDevelopmentDigest 企业发展各维度摘要
func buildDevelopmentDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = count > 0

	switch key {
	case "core_team":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["team_count"] = len(list)
			}
		}
	case "financing_history":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["round_count"] = len(list)
				// 提取最新一轮
				if len(list) > 0 {
					if first, ok := list[0].(map[string]any); ok {
						for _, f := range []string{"roundName", "investMoney", "investDate"} {
							if v, ok := first[f]; ok {
								digest["latest_"+f] = v
							}
						}
					}
				}
			}
		}
	case "competitors":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["competitor_count"] = len(list)
			}
		}
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// EnterpriseDevelopment 企业发展：并行聚合 5 个发展/融资接口
func (s *Services) EnterpriseDevelopment(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, developmentSections, entName, companyID, 10, buildDevelopmentDigest)
	return buildPhase6Output("enterprise_development", entity, results, okCount, skippedCount, len(developmentSections), elapsed)
}

// ═══════════════════════════ 7. 投资事件画像 ═══════════════════════════

var investmentEventSections = []phase6Section{
	{Key: "investment_events", Pcode: "E_023_0031", Title: "投资事件", ParamKey: "entName"},
	{Key: "investment_trend", Pcode: "E_023_0041", Title: "投资趋势统计", ParamKey: "entName"},
	{Key: "partner_ranking", Pcode: "E_023_0051", Title: "合作机构排名", ParamKey: "entName"},
	{Key: "partners", Pcode: "E_023_0061", Title: "合作机构", ParamKey: "entName"},
	{Key: "investment_org_detail", Pcode: "E_011_0311", Title: "投资机构详情", ParamKey: "name"},
}

// InvestmentEventProfile 投资事件画像：并行聚合 5 个投资接口
func (s *Services) InvestmentEventProfile(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, investmentEventSections, entName, companyID, 10, buildGenericCountDigest)
	return buildPhase6Output("investment_event_profile", entity, results, okCount, skippedCount, len(investmentEventSections), elapsed)
}

// ═══════════════════════════ 8. 企业评价报告 ═══════════════════════════

var evaluationReportSections = []phase6Section{
	{Key: "founding_team", Pcode: "E_018_0091", Title: "创始团队", ParamKey: "entName"},
	{Key: "tech_competitiveness", Pcode: "E_018_0101", Title: "创新技术竞争力", ParamKey: "entName"},
	{Key: "industry_saturation", Pcode: "E_018_0111", Title: "行业饱和度", ParamKey: "province"},
	{Key: "industry_monopoly", Pcode: "E_018_0121", Title: "行业垄断程度", ParamKey: "province"},
	{Key: "industry_growth", Pcode: "E_018_0131", Title: "行业成长情况", ParamKey: "province"},
}

// buildEvaluationDigest 企业评价报告各维度摘要
func buildEvaluationDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = hasAnyData(parsed)

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	}

	switch key {
	case "founding_team":
		if data != nil {
			for _, f := range []string{"founderName", "education", "experience"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}
	case "tech_competitiveness":
		if data != nil {
			for _, f := range []string{"score", "rank", "level"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}
	case "industry_saturation", "industry_monopoly", "industry_growth":
		if data != nil {
			for _, f := range []string{"score", "level", "description", "industry"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// EnterpriseEvaluationReport 企业评价报告：并行聚合 5 个评价接口
// 注意：前 2 个接口使用 entName，后 3 个使用 province（行业维度）
func (s *Services) EnterpriseEvaluationReport(ctx context.Context, entName, companyID string) string {
	return s.EnterpriseEvaluationReportWithRegion(ctx, entName, companyID, "", "", "", "")
}

func (s *Services) EnterpriseEvaluationReportWithRegion(ctx context.Context, entName, companyID, province, city, county, economicRegion string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	province = strings.TrimSpace(province)
	city = strings.TrimSpace(city)
	county = strings.TrimSpace(county)
	economicRegion = strings.TrimSpace(economicRegion)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	if province == "" && (city != "" || county != "" || economicRegion != "") {
		return mustJSON(map[string]any{"error": "validation", "message": "提供 city/county/economic_region 时必须同时提供 province"})
	}

	start := time.Now()
	svc := s // goroutine 内 s 被 section 形参遮蔽，此处显式捕获接收器
	results := make([]map[string]any, 0, len(evaluationReportSections))
	var wg sync.WaitGroup
	var mu sync.Mutex
	apiCalls := 0

	for _, sec := range evaluationReportSections {
		if sec.ParamKey == "province" && province == "" {
			// ⚠️ 必须与 goroutine 内的 append 使用同一把锁：此前主 goroutine 无锁 append
			// 与并发 goroutine 的加锁 append 竞争同一 slice 底层数组（-race 检出）
			mu.Lock()
			results = append(results, map[string]any{
				"key":    sec.Key,
				"pcode":  sec.Pcode,
				"title":  sec.Title,
				"status": "skipped",
				"reason": "missing_region",
			})
			mu.Unlock()
			continue
		}
		apiCalls++
		wg.Add(1)
		go func(s phase6Section) {
			defer wg.Done()
			params := map[string]any{"pageNo": 1, "pageSize": 10}
			// 企业维度接口
			if s.ParamKey == "entName" {
				if entName != "" {
					params["entName"] = entName
				}
				if companyID != "" {
					params["companyId"] = companyID
				}
			}
			// 行业维度接口：仅在提供真实地区信息时执行
			if s.ParamKey == "province" {
				params["province"] = province
				if city != "" {
					params["city"] = city
				}
				if county != "" {
					params["county"] = county
				}
				if economicRegion != "" {
					params["economicRegion"] = economicRegion
				}
			}
		raw := svc.callQuery(ctx, s.Pcode, params)
		item := map[string]any{"key": s.Key, "pcode": s.Pcode, "title": s.Title}
		if parsed, errMsg := decodeAggregatePayload(raw); errMsg == "" {
			item["status"] = "ok"
			item["data"] = parsed
			dgEv := buildEvaluationDigest(s.Key, parsed)
			enrichDigestPaging(parsed, dgEv)
			item["digest"] = dgEv
			} else {
				item["status"] = "error"
				item["error"] = errMsg
			}
			mu.Lock()
			results = append(results, item)
			mu.Unlock()
		}(sec)
	}
	wg.Wait()

	okCount := 0
	for _, r := range results {
		if r["status"] == "ok" {
			okCount++
		}
	}

	out := map[string]any{
		"entity":      entName,
		"sections":    results,
		"elapsed_ms":  time.Since(start).Milliseconds(),
		"api_calls":   apiCalls,
		"total_count": len(evaluationReportSections),
		"ok_count":    okCount,
		"error_count": apiCalls - okCount,
		"skipped_count": len(evaluationReportSections) - apiCalls,
		"region": map[string]any{"province": province, "city": city, "county": county, "economic_region": economicRegion},
		"_note":       "行业维度接口（行业饱和度/垄断程度/成长情况）仅在提供真实地区参数时执行；未提供地区时将跳过，避免用企业名称误填 province。",
	}
	return mustJSON(out)
}

// ─────────────────────────── 统一响应信封 ───────────────────────────

// buildToolOutput 构建标准化聚合工具响应信封。
// 统一输出格式：tool + status + api_calls + elapsed_ms + extra 字段。
// status 根据 okCount/errorCount 自动判定：全成功=ok、部分成功=partial、全失败=error、无调用=no_data。
func buildToolOutput(tool string, apiCalls int, start time.Time, extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra)+4)
	for k, v := range extra {
		out[k] = v
	}
	out["tool"] = tool
	out["api_calls"] = apiCalls
	out["elapsed_ms"] = time.Since(start).Milliseconds()
	okCount, _ := extra["ok_count"].(int)
	errCount, _ := extra["error_count"].(int)
	switch {
	case okCount > 0 && errCount == 0:
		out["status"] = "ok"
	case okCount > 0:
		out["status"] = "partial"
	case errCount > 0:
		out["status"] = "error"
	default:
		if _, has := extra["status"]; !has {
			out["status"] = "ok"
		}
	}
	return out
}

func decodeAggregatePayload(raw string) (map[string]any, string) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, "响应解析失败"
	}
	if errStr, ok := parsed["error"].(string); ok && errStr != "" {
		msg, _ := parsed["message"].(string)
		if strings.TrimSpace(msg) != "" {
			return parsed, errStr + ": " + msg
		}
		return parsed, errStr
	}
	return parsed, ""
}

// ─────────────────────────── annual_report_detail 级联工具 ───────────────────────────

// annualReportSubSections 年报子表 7 维度（ParamKey 均为 id）
var annualReportSubSections = []phase6Section{
	{Key: "change", Pcode: "E_007_3001", Title: "年报变更", ParamKey: "id"},
	{Key: "shareholder_change", Pcode: "E_007_3101", Title: "年报股东股权变更", ParamKey: "id"},
	{Key: "investment", Pcode: "E_007_3201", Title: "年报对外投资", ParamKey: "id"},
	{Key: "shareholder_info", Pcode: "E_007_3301", Title: "年报股东及出资信息", ParamKey: "id"},
	{Key: "social_insurance", Pcode: "E_007_3401", Title: "年报社保", ParamKey: "id"},
	{Key: "website", Pcode: "E_007_3501", Title: "年报网站", ParamKey: "id"},
	{Key: "guarantee", Pcode: "E_007_3601", Title: "年报对外提供担保信息", ParamKey: "id"},
}

// AnnualReportDetail 年报详情级联工具：先查年报列表获取 reportID，再并行查 7 个子表
func (s *Services) AnnualReportDetail(ctx context.Context, entName, companyID, reportID string) string {
	start := time.Now()
	reportIDSource := "user"
	listCall := 0 // 是否产生年报列表查询调用（用于计费统计）

	// 如果未提供 reportID，先调 E_007_2901 获取最新年报 id
	if strings.TrimSpace(reportID) == "" {
		reportIDSource = "auto"
		listCall = 1
		p := map[string]any{"pageNo": 1, "pageSize": 1}
		if entName != "" {
			p["entName"] = entName
		}
		if companyID != "" {
			p["companyId"] = companyID
		}
		raw := s.callQuery(ctx, "E_007_2901", p)
		parsed, errMsg := decodeAggregatePayload(raw)
		if errMsg != "" {
			return mustJSON(map[string]any{"tool": "annual_report_detail", "status": "error", "error": "年报列表查询失败: " + errMsg})
		}
		// 尝试提取 data.list[0].id
		if data, ok := parsed["data"].(map[string]any); ok {
			if list, ok := data["list"].([]any); ok && len(list) > 0 {
				if first, ok := list[0].(map[string]any); ok {
					if id, ok := first["id"].(string); ok && id != "" {
						reportID = id
					} else if id, ok := first["id"].(float64); ok {
						reportID = fmt.Sprintf("%.0f", id)
					}
				}
			}
		}
		if reportID == "" {
			return mustJSON(map[string]any{
				"tool": "annual_report_detail", "status": "no_data",
				"error": "未找到年报记录，请确认企业名称是否正确", "api_calls": 1,
			})
		}
	}

	// 并行查 7 个子表（ParamKey 均为 id）
	results := make([]map[string]any, len(annualReportSubSections))
	var wg sync.WaitGroup
	for i, sec := range annualReportSubSections {
		wg.Add(1)
		go func(idx int, sec phase6Section) {
			defer wg.Done()
			params := map[string]any{"pageNo": 1, "pageSize": 20, sec.ParamKey: reportID}
			raw := s.callQuery(ctx, sec.Pcode, params)
			item := map[string]any{"key": sec.Key, "pcode": sec.Pcode, "title": sec.Title}
			if parsed, errMsg := decodeAggregatePayload(raw); errMsg == "" {
				item["status"] = "ok"
				item["data"] = parsed
				item["digest"] = map[string]any{
					"count": extractCount(parsed),
				}
			} else {
				item["status"] = "error"
				item["error"] = errMsg
			}
			results[idx] = item
		}(i, sec)
	}
	wg.Wait()

	okCount := 0
	for _, r := range results {
		if r["status"] == "ok" {
			okCount++
		}
	}

	out := buildToolOutput("annual_report_detail", len(annualReportSubSections)+listCall, start, map[string]any{
		"entity":           entName,
		"report_id":        reportID,
		"report_id_source": reportIDSource,
		"sections":         results,
		"total_count":      len(annualReportSubSections),
		"ok_count":         okCount,
		"error_count":      len(annualReportSubSections) - okCount,
		"_note":            "report_id 可通过 annual_report_investments 工具预先获取；传入 report_id 可跳过列表查询节省 1 次调用。",
	})
	return mustJSON(out)
}

// ─────────────────────────── industrial_park_search 园区搜索 ───────────────────────────

// IndustrialParkSearch 产业园区搜索：按名称/地域搜索园区及其入驻企业
func (s *Services) IndustrialParkSearch(ctx context.Context, parkName, province, city string) string {
	return s.IndustrialParkSearchWithID(ctx, parkName, "", province, city)
}

func (s *Services) IndustrialParkSearchWithID(ctx context.Context, parkName, parkID, province, city string) string {
	start := time.Now()
	apiCalls := 0

	out := map[string]any{
		"tool":       "industrial_park_search",
		"park_name":  parkName,
		"park_id":    parkID,
		"province":   province,
		"city":       city,
	}

	// 园区名查询：先查园区列表与分布，再基于 parkId 查企业列表
	if strings.TrimSpace(parkName) != "" || strings.TrimSpace(parkID) != "" {
		nameResults := make([]map[string]any, 0, 3)
		selectedParkID := strings.TrimSpace(parkID)

		if strings.TrimSpace(parkName) != "" {
			apiCalls++
			listRaw := s.callQuery(ctx, "E_007_0971", map[string]any{"pageNo": 1, "pageSize": 20, "parkName": parkName})
			listItem := map[string]any{"key": "park_list", "pcode": "E_007_0971", "title": "园区列表"}
			if parsed, errMsg := decodeAggregatePayload(listRaw); errMsg == "" {
				listItem["status"] = "ok"
				listItem["data"] = parsed
				listItem["digest"] = map[string]any{"count": extractCount(parsed)}
				if selectedParkID == "" {
					selectedParkID = chooseParkID(parsed, parkName)
				}
			} else {
				listItem["status"] = "error"
				listItem["error"] = errMsg
			}
			nameResults = append(nameResults, listItem)

			apiCalls++
			distRaw := s.callQuery(ctx, "E_007_1011", map[string]any{"parkName": parkName, "pageNo": 1, "pageSize": 20})
			distItem := map[string]any{"key": "park_distribution", "pcode": "E_007_1011", "title": "园区企业分布"}
			if parsed, errMsg := decodeAggregatePayload(distRaw); errMsg == "" {
				distItem["status"] = "ok"
				distItem["data"] = parsed
				distItem["digest"] = map[string]any{"count": extractCount(parsed)}
			} else {
				distItem["status"] = "error"
				distItem["error"] = errMsg
			}
			nameResults = append(nameResults, distItem)
		}

		enterprisesItem := map[string]any{"key": "park_enterprises", "pcode": "E_007_0991", "title": "园区企业列表"}
		if selectedParkID != "" {
			apiCalls++
			enterprisesRaw := s.callQuery(ctx, "E_007_0991", map[string]any{"pageNo": 1, "pageSize": 20, "parkId": selectedParkID})
			if parsed, errMsg := decodeAggregatePayload(enterprisesRaw); errMsg == "" {
				enterprisesItem["status"] = "ok"
				enterprisesItem["data"] = parsed
				enterprisesItem["digest"] = map[string]any{"count": extractCount(parsed)}
			} else {
				enterprisesItem["status"] = "error"
				enterprisesItem["error"] = errMsg
			}
		} else {
			enterprisesItem["status"] = "skipped"
			enterprisesItem["reason"] = "missing_park_id"
		}
		nameResults = append(nameResults, enterprisesItem)

		okCount := 0
		for _, r := range nameResults {
			if r["status"] == "ok" {
				okCount++
			}
		}
		out["resolved_park_id"] = selectedParkID
		out["name_results"] = nameResults
		out["name_ok_count"] = okCount
	}

	// 按地域筛选
	if strings.TrimSpace(province) != "" || strings.TrimSpace(city) != "" {
		apiCalls++
		params := map[string]any{"pageNo": 1, "pageSize": 20}
		if province != "" {
			params["province"] = province
		}
		if city != "" {
			params["city"] = city
		}
		raw := s.callQuery(ctx, "E_007_1041", params)
		if parsed, errMsg := decodeAggregatePayload(raw); errMsg == "" {
			out["location_filter"] = map[string]any{
				"pcode":  "E_007_1041",
				"title":  "园区筛选",
				"status": "ok",
				"data":   parsed,
				"digest": map[string]any{"count": extractCount(parsed)},
			}
		} else {
			out["location_filter"] = map[string]any{
				"pcode": "E_007_1041", "title": "园区筛选",
				"status": "error", "error": errMsg,
			}
		}
	}

	if apiCalls == 0 {
		out["status"] = "error"
		out["error"] = "请提供 park_name、park_id 或 province/city 至少一个参数"
		return mustJSON(out)
	}

	out["api_calls"] = apiCalls
	out["elapsed_ms"] = time.Since(start).Milliseconds()
	out["_note"] = "园区详情(parkId)可通过 keeprisk_query(E_007_0981, {parkId: xxx}) 单独查询。"
	out["status"] = "ok"
	return mustJSON(out)
}

func chooseParkID(parsed map[string]any, parkName string) string {
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		return ""
	}
	list, ok := data["list"].([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	if len(list) == 1 {
		if item, ok := list[0].(map[string]any); ok {
			if id, ok := item["id"].(string); ok {
				return id
			}
		}
	}
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["parkName"].(string)
		if strings.TrimSpace(name) == strings.TrimSpace(parkName) {
			if id, ok := item["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}
