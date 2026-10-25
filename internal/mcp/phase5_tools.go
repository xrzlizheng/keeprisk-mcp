package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ═══════════════════════════ 1. 上市公司财务数据 ═══════════════════════════

// 上市公司财务数据聚合 13 个 API（全部使用 companyName；PDF：number 选填，传 0 返回全部）
var listedFinSections = []phase6Section{
	{Key: "balance_sheet", Pcode: "E_010_0211", Title: "资产负债表", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "annual_financials", Pcode: "E_010_0221", Title: "年度财务数据", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "profit_distribution", Pcode: "E_010_0231", Title: "利润分配表", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "quarterly_cashflow", Pcode: "E_010_0251", Title: "单季现金流量表", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "quarterly_income", Pcode: "E_010_0331", Title: "单季利润表", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "quarterly_financials", Pcode: "E_010_0341", Title: "单季度财务数据", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "report_period_financials", Pcode: "E_010_0431", Title: "报告期财务数据", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "cashflow_statement", Pcode: "E_010_0581", Title: "现金流量表", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	// 2026-09-18 文档核对：E_010_0901 声明 companyname(文档笔误，网关实为 companyName)、
	// industry 必填、selectType 必填(固定值 10041)、sortfield/sorttype/indextype 选填；
	// **没有 number**——网关实测回"未知参数 number"。旧配置传 number=0 且漏传必填 selectType。
	{Key: "financial_ratios", Pcode: "E_010_0901", Title: "财务比率数据", ParamKey: "companyName", ExtraParams: map[string]any{"selectType": "10041"}},
	{Key: "dividend_bonus", Pcode: "E_010_0181", Title: "分红转增", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "dividend_rights", Pcode: "E_010_0191", Title: "分红配股", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "fundraising_stats", Pcode: "E_010_0561", Title: "上市以来募资统计", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "additional_issuance", Pcode: "E_010_0551", Title: "增发数据", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
}

// ListedCompanyFinancials 上市公司财务数据：并行聚合 13 个财务接口
func (s *Services) ListedCompanyFinancials(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, listedFinSections, entName, companyID, 5, buildGenericCountDigest)
	return buildPhase6Output("listed_company_financials", entity, results, okCount, skippedCount, len(listedFinSections), elapsed)
}

// ═══════════════════════════ 2. 上市公司市场交易数据 ═══════════════════════════

// 上市公司市场交易数据聚合 15 个 API（PDF 主体多为 companyName）。
// 注意：全市场列表类接口（E_010_0001 全部A股、E_010_1301 退市公司）对企业维度
// 画像无意义且每次必打网关，已从本工具移除；如需全市场数据请 keeprisk_query 直调。
var listedMarketSections = []phase6Section{
	{Key: "equity_pledge_detail", Pcode: "E_010_0411", Title: "股权质押明细", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "lockup_expiry", Pcode: "E_010_0421", Title: "限售解禁", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "suspension_history", Pcode: "E_010_0441", Title: "停牌历史", ParamKey: "companyName"},
	{Key: "suspension_realtime", Pcode: "E_010_0701", Title: "停牌实时数据", ParamKey: "_", SkipReason: "missing_innercode"},
	{Key: "shareholder_count", Pcode: "E_010_0271", Title: "股东户数", ParamKey: "companyName", ExtraParams: map[string]any{"timerange": "4", "number": "0"}},
	{Key: "institutional_holdings", Pcode: "E_010_0281", Title: "机构持股", ParamKey: "companyName"},
	{Key: "shareholders_vs_price", Pcode: "E_010_0461", Title: "股东人数与股价比较", ParamKey: "companyName", ExtraParams: map[string]any{"number": "0"}},
	{Key: "margin_buying", Pcode: "E_010_0471", Title: "融资买入", ParamKey: "companyName", ExtraParams: map[string]any{"timerange": "4"}},
	{Key: "short_selling", Pcode: "E_010_0481", Title: "融券卖出", ParamKey: "companyName", ExtraParams: map[string]any{"timerange": "4"}},
	{Key: "margin_balance", Pcode: "E_010_1001", Title: "融资融券余额", ParamKey: "companyName", ExtraParams: map[string]any{"selectType": "10028", "timerange": "6", "number": "0"}},
	{Key: "block_trades", Pcode: "E_010_0531", Title: "大宗交易", ParamKey: "companyName", ExtraParams: map[string]any{"type": "0"}},
	{Key: "dragon_tiger", Pcode: "E_010_0351", Title: "龙虎榜明细", ParamKey: "companyName"},
	{Key: "dragon_tiger_seats", Pcode: "E_010_0451", Title: "龙虎榜席位统计", ParamKey: "companyName"},
	{Key: "institutional_seats", Pcode: "E_010_0511", Title: "机构席位统计", ParamKey: "companyName"},
	{Key: "institutional_seat_trades", Pcode: "E_010_0521", Title: "机构席位交易明细", ParamKey: "companyName"},
}

// ListedCompanyMarket 上市公司市场交易数据：并行聚合 15 个市场/交易接口
func (s *Services) ListedCompanyMarket(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, listedMarketSections, entName, companyID, 5, buildGenericCountDigest)
	return buildPhase6Output("listed_company_market", entity, results, okCount, skippedCount, len(listedMarketSections), elapsed)
}

// ═══════════════════════════ 3. 企业年报与对外投资 ═══════════════════════════

// 企业年报与对外投资聚合 4 个 API
//
// 2026-09-18：两个财务子表的 year 传入方式不同，必须分别处理——
//   - E_007_0931 企业财务年报：year **必填**，不传稳定 110；需注入默认期。
//   - E_007_0941 企业财务年报指标：year 选填，不传时上游**按固定默认期**返回
//     （实测省略 year 与显式 year=2023 逐字节一致），故不注入也能出数，
//     但需在 digest 里标注"期次非最新"以免被误读（见 buildAnnualDigest 的 period_note）。
var annualReportSections = []phase6Section{
	{Key: "annual_report", Pcode: "E_007_2901", Title: "企业年报", ParamKey: "entName"},
	{Key: "financial_annual", Pcode: "E_007_0931", Title: "企业财务年报", ParamKey: "entName"},
	{Key: "financial_annual_indicators", Pcode: "E_007_0941", Title: "企业财务年报指标", ParamKey: "entName"},
	{Key: "external_investment_v2", Pcode: "E_017_0191", Title: "对外投资V2", ParamKey: "companyName"},
}

// defaultFinancialYear 财务年报子表的默认报告期。
//
// 取「当前年 - 3」：工商年报数据通常滞后 1–2 年，实测 2026-09 时可取到 2023 期
// （2024/2025 均 110）。与 credit_score 的财务评分共用同一口径。
func defaultFinancialYear() string {
	return strconv.Itoa(time.Now().Year() - 3)
}

// AnnualReportInvestments 企业年报与对外投资：并行聚合 4 个年报/投资接口。
// year 为空时财务子表使用默认期 defaultFinancialYear()。
func (s *Services) AnnualReportInvestments(ctx context.Context, entName, companyID, year string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	year = strings.TrimSpace(year)
	if year == "" {
		year = defaultFinancialYear()
	}
	// 仅在财务子表注入 year（其余两个接口不声明该参数，注了会被网关拒绝并产生告警）
	sections := make([]phase6Section, 0, len(annualReportSections))
	for _, sec := range annualReportSections {
		if sec.Key == "financial_annual" || sec.Key == "financial_annual_indicators" {
			sec.ExtraParams = map[string]any{"year": year}
		}
		sections = append(sections, sec)
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, sections, entName, companyID, 10, buildAnnualDigest)
	out := buildPhase6Output("annual_report_investments", entity, results, okCount, skippedCount, len(sections), elapsed)
	// 期次声明：财务数据的报告期由入参决定，而**上游响应本身不含期次字段**，
	// 不回告调用方就无从判断这批数字属于哪一年（这正是第 18 条的成因）。
	return mergeJSONFields(out, map[string]any{
		"financial_year": year,
		"financial_year_note": "「企业财务年报 / 企业财务年报指标」两节的报告期；" +
			"上游响应不含期次字段，此为本服务实际请求的报告期",
	})
}

// mergeJSONFields 把附加字段合并进已序列化的 JSON 对象（解析失败时原样返回）。
// 用于聚合工具在通用输出之外追加口径声明，避免为每个工具重写序列化逻辑。
func mergeJSONFields(out string, extra map[string]any) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return out
	}
	for k, v := range extra {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return out
	}
	return string(b)
}

// financialAnnualField 企业财务年报（E_007_0931）单个金额字段的中文名。
//
// 依据接口文档 data 节点说明逐条抄录。**必须带中文名输出**：
// 该接口的英文键名有明确误导性——`retained_profit` 文档中文是"净利润"，
// 而英文直译是"留存收益/未分配利润"，两者是完全不同的财务概念，
// 只给英文键会让 Agent 按字面量读错（与第 22 条 totalSales/totalTax 同类问题）。
type financialAnnualField struct {
	Name  string
	Label string
}

var financialAnnualFields = []financialAnnualField{
	{"total_assets", "资产总额"},
	{"total_liability", "负债总额"},
	{"total_equity", "所有者权益"},
	{"total_sales", "营业收入"},
	{"prime_bus_profit", "主营业务收入"},
	{"total_profit", "利润总额"},
	{"retained_profit", "净利润（注意：英文名 retained_profit 非「留存收益」）"},
	{"total_tax", "纳税总额"},
}

func financialAnnualFieldLabels() map[string]string {
	out := make(map[string]string, len(financialAnnualFields))
	for _, f := range financialAnnualFields {
		out[f.Name] = f.Label
	}
	return out
}

// buildAnnualDigest 企业年报各维度摘要
func buildAnnualDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)

	switch key {
	case "annual_report":
		// 字段名以接口文档 + 实测响应为准（2026-09-18 核对 E_007_2901 data 节点说明）：
		// 旧实现找 id/year/entName/regNo，而实际字段是
		// id(年报id) / reportYear(年报年度) / companyName(企业名称) / regNumber(注册号)
		// —— 其中 year/entName/regNo 三个**不存在**，从未取到值。
		// 另附年报自带的财务字段（文档均有；注意 retainedProfit 此处同样是"净利润"）。
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			for _, f := range []string{"id", "reportYear", "companyName", "regNumber"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
			if list, ok := data["list"].([]any); ok && len(list) > 0 {
				if first, ok := list[0].(map[string]any); ok {
					for _, f := range []string{"totalAssets", "totalLiability", "totalSales",
						"totalProfit", "retainedProfit"} {
						if v, ok := first[f]; ok {
							digest[f] = v
						}
					}
				}
				digest["annual_financial_labels"] = map[string]string{
					"totalAssets": "资产总额", "totalLiability": "负债总额",
					"totalSales": "销售总额(营业总收入)", "totalProfit": "利润总额",
					"retainedProfit": "净利润",
				}
			}
		}
	case "financial_annual":
		// 字段名与含义以接口文档为准（2026-09-18，E_007_0931 原文 data 节点说明）：
		//
		//	total_assets      资产总额     区间范围
		//	total_liability   负债总额     区间范围
		//	total_sales       营业收入     区间范围
		//	prime_bus_profit  主营业务收入  区间范围
		//	total_profit      利润总额     区间范围
		//	retained_profit   净利润       区间范围   ← 英文名易误读为"留存收益"，实为净利润
		//	total_tax         纳税总额     区间范围
		//	total_equity      所有者权益    区间范围
		//
		// 旧实现找 totalAssets/totalRevenue/netProfit/year，**8 个字段无一命中**，摘要恒为空。
		// 取值形如 "4000,5000" —— 文档明示为**区间范围**，即 [下界, 上界]，不是"两个不明数值"。
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			var got []string
			for _, f := range financialAnnualFields {
				if v, ok := data[f.Name]; ok {
					digest[f.Name] = v
					got = append(got, f.Name)
				}
			}
			if len(got) > 0 {
				digest["field_labels"] = financialAnnualFieldLabels()
				digest["value_format"] = "区间范围：\"下界,上界\"（如 \"4000,5000\" 表示 4000–5000）；" +
					"单位上游未注明，字段含义见 field_labels"
			}
		}
	case "financial_annual_indicators":
		// 字段名以实测响应为准（2026-09-18）：旧实现找 roe/roa/grossMargin/netMargin/year，
		// 而 E_007_0941 实际返回 assetLiRate/equityMult/equityRate/grossProfitRate/profitRate/
		// taxBurdenRate/returnAssetRate 等 15 项，**5 个字段无一命中**。
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			for _, f := range []string{"assetLiRate", "returnAssetRate", "profitRate",
				"grossProfitRate", "equityMult", "taxBurdenRate"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
			// equityRate 是 equityMult × assetLiRate 的派生量（实测 3 企业 × 13 年份恒等），
			// 无独立信息量且字面"权益率"易被误读为 权益/资产（实为 负债/权益），故只给标注不取值。
			if _, has := data["equityRate"]; has {
				digest["equityRate_note"] = "派生量 = equityMult × assetLiRate（实为负债/权益，非权益/资产），无独立信息量"
			}
			// 期次缺失提示：E_007_0941 响应不含 year，省略入参时上游固定回落某一期
			digest["period_note"] = "本接口响应不含期次字段；未显式传 year 时上游按固定默认期返回，请勿当作最新报告期"
		}
	case "external_investment_v2":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["investment_count"] = len(list)
			}
		}
	}

	hd := hasAnyData(parsed)
	// 仅在有真实条数、或确为无数据时写 record_count；
	// 「有数据但该接口不提供条数口径」时省略，避免 has_data=true 与 record_count=0 自相矛盾
	if count > 0 || !hd {
		digest["record_count"] = count
	}
	digest["has_data"] = hd
	if total, ok := extractTotal(parsed); ok {
		digest["total"] = total
	}
	pc := 0
	if v := extractPageCount(parsed); v > 0 && v != count {
		digest["page_count"] = v
		pc = v
	}
	digest["summary"] = digestSummary(key, count, pc, hd)
	return digest
}

// ═══════════════════════════ 4. 知识产权组合 ═══════════════════════════

// 知识产权组合聚合 5 个 API（全部使用 name）
var ipSections = []phase6Section{
	{Key: "patents", Pcode: "E_012_0001", Title: "专利", ParamKey: "name"},
	{Key: "software_copyrights", Pcode: "E_012_0101", Title: "软件著作权", ParamKey: "name"},
	{Key: "works_copyrights", Pcode: "E_012_0201", Title: "作品著作权", ParamKey: "name"},
	{Key: "domains", Pcode: "E_012_0301", Title: "网址域名备案", ParamKey: "name"},
	{Key: "trademarks", Pcode: "E_012_0401", Title: "商标", ParamKey: "name"},
}

// IntellectualPropertyPortfolio 知识产权组合：并行聚合 5 个知识产权接口
func (s *Services) IntellectualPropertyPortfolio(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, _, _, elapsed := runPhase6Aggregate(s, ctx, ipSections, entName, companyID, 10, buildIPDigest)
	// 汇总知识产权统计
	ipSummary := buildIPSummary(results)
	ipSummary["entity"] = entity
	ipSummary["sections"] = results
	ipSummary["elapsed_ms"] = elapsed
	ipSummary["api_calls"] = len(ipSections)
	return mustJSON(ipSummary)
}

// buildIPDigest 知识产权各维度摘要
func buildIPDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = count > 0

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	}

	switch key {
	case "patents":
		if data != nil {
			// 尝试提取专利类型分布
			if list, ok := data["list"].([]any); ok {
				typeCount := map[string]int{}
				for _, p := range list {
					if pm, ok := p.(map[string]any); ok {
						pt, _ := pm["patentType"].(string)
						if pt == "" {
							pt, _ = pm["type"].(string)
						}
						if pt != "" {
							typeCount[pt]++
						}
					}
				}
				if len(typeCount) > 0 {
					digest["type_distribution"] = typeCount
				}
			}
		}
	case "trademarks":
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				statusCount := map[string]int{}
				for _, t := range list {
					if tm, ok := t.(map[string]any); ok {
						st, _ := tm["status"].(string)
						if st != "" {
							statusCount[st]++
						}
					}
				}
				if len(statusCount) > 0 {
					digest["status_distribution"] = statusCount
				}
			}
		}
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// buildIPSummary 汇总知识产权整体统计
func buildIPSummary(results []map[string]any) map[string]any {
	summary := map[string]any{
		"total_patents":             0,
		"total_software_copyrights": 0,
		"total_works_copyrights":    0,
		"total_domains":             0,
		"total_trademarks":          0,
	}
	totalIP := 0
	for _, r := range results {
		if r["status"] != "ok" {
			continue
		}
		key, _ := r["key"].(string)
		digest, _ := r["digest"].(map[string]any)
		count, _ := digest["record_count"].(int)
		totalIP += count
		switch key {
		case "patents":
			summary["total_patents"] = count
		case "software_copyrights":
			summary["total_software_copyrights"] = count
		case "works_copyrights":
			summary["total_works_copyrights"] = count
		case "domains":
			summary["total_domains"] = count
		case "trademarks":
			summary["total_trademarks"] = count
		}
	}
	summary["total_ip_count"] = totalIP
	summary["has_ip"] = totalIP > 0
	return summary
}

// ═══════════════════════════ 5. 企业关联图谱 ═══════════════════════════

// 企业关联图谱聚合：图谱 + 股权穿透（股东/对外投资，PDF type 必填）+ 实质图谱
var graphRelationSections = []phase6Section{
	{Key: "enterprise_graph", Pcode: "E_007_0401", Title: "企业图谱", ParamKey: "entName"},
	{Key: "equity_penetration_holders", Pcode: "E_007_0631", Title: "股权穿透-股东节点", ParamKey: "entName", ExtraParams: map[string]any{"type": 1}},
	{Key: "equity_penetration_investments", Pcode: "E_007_0631", Title: "股权穿透-对外投资节点", ParamKey: "entName", ExtraParams: map[string]any{"type": 2}},
	{Key: "substantive_graph", Pcode: "E_007_5101", Title: "实质图谱", ParamKey: "entName"},
}

// EnterpriseGraphRelations 企业关联图谱：并行聚合图谱/穿透/实质关联接口
func (s *Services) EnterpriseGraphRelations(ctx context.Context, entName, companyID string, digestOnly bool) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, graphRelationSections, entName, companyID, 20, buildGraphDigest)
	// 图类原始载荷可达数十 KB（实测 48KB），digest_only 默认省略 data，
	// 摘要已含节点/边计数与 top 关系，足够判断"有没有关联、关联多深"。
	if digestOnly {
		for _, sec := range results {
			delete(sec, "data")
		}
	}
	out := buildPhase6Output("enterprise_graph_relations", entity, results, okCount, skippedCount, len(graphRelationSections), elapsed)
	if digestOnly {
		if m, ok := jsonRaw(out).(map[string]any); ok {
			m["digest_only"] = true
			m["_note"] = "digest_only=true：仅返回各图摘要（节点/边计数），已省略原始 nodes/links 数组（完整图约数十 KB）。需要全图时显式传 digest_only=false"
			out = mustJSON(m)
		}
	}
	return out
}

// buildGraphDigest 企业关联图谱各维度摘要
//
// 2026-09-18 修正（飞友科技实测）：图类接口的载荷键是 nodes/links/edges/relationships，
// 旧实现用 extractCount（只认 list/items）算 record_count → 恒为 0 → has_data=false，
// 于是"143 节点 + 213 关系"的图谱被摘要成"0 条记录"，依赖 digest 路由的 Agent 会跳过关联分析。
// 现改为：图类 record_count = 节点数 + 边数（两者都是记录），has_data 走统一证据判定。
func buildGraphDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	}

	count := extractCount(parsed)

	switch key {
	case "enterprise_graph", "substantive_graph":
		if data != nil {
			// E_007_0401 企业图谱的载荷在 data.result 下（文档：`result 结果集`，
			// 其下 `relationships 节点关系` / `nodes 节点`；实测飞友科技 nodes=143、
			// relationships=213 全在 result 里）。旧实现直接在 data 下找 →
			// node_count/relationship_count 从未取到，record_count 恒 0。
			if result, ok := data["result"].(map[string]any); ok {
				data = result
			}
			nodeN, edgeN := 0, 0
			if nodes, ok := data["nodes"].([]any); ok {
				digest["node_count"] = len(nodes)
				nodeN = len(nodes)
			}
			if links, ok := data["links"].([]any); ok {
				digest["link_count"] = len(links)
				edgeN += len(links)
			}
			if edges, ok := data["edges"].([]any); ok {
				digest["edge_count"] = len(edges)
				edgeN += len(edges)
			}
			if relations, ok := data["relations"].([]any); ok {
				digest["relation_count"] = len(relations)
				edgeN += len(relations)
			}
			// relationships 是 E_007_0401 企业图谱的官方键名（文档：`relationships 节点关系`）
			if rels, ok := data["relationships"].([]any); ok {
				digest["relationship_count"] = len(rels)
				edgeN += len(rels)
			}
			// 图类：记录数按 节点+边 计（两者都是返回的记录）
			if nodeN+edgeN > 0 {
				count = nodeN + edgeN
			}
		}
	case "equity_penetration_holders", "equity_penetration_investments", "equity_penetration_nodes":
		if data != nil {
			if investors, ok := data["investorList"].([]any); ok {
				digest["investor_count"] = len(investors)
			}
			if holders, ok := data["holderList"].([]any); ok {
				digest["holder_count"] = len(holders)
			}
			if list, ok := data["list"].([]any); ok {
				digest["node_count"] = len(list)
			}
			if nodes, ok := data["nodes"].([]any); ok {
				digest["node_count"] = len(nodes)
			}
		}
	}

	hd := hasAnyData(parsed)
	digest["record_count"] = count
	digest["has_data"] = hd
	if total, ok := extractTotal(parsed); ok {
		digest["total"] = total
	}
	// 图类不输出 page_count：record_count 是"节点+边"合计，"本页 N 条"表述不成立
	if _, isGraph := digest["node_count"]; isGraph {
		digest["summary"] = fmt.Sprintf("%s: %d 条（节点 %d + 边 %d）", key, count,
			digest["node_count"], graphEdgeCount(digest))
	} else {
		digest["summary"] = digestSummary(key, count, 0, hd)
	}
	return digest
}

// graphEdgeCount 从 digest 里汇总已记录的边数字段。
func graphEdgeCount(digest map[string]any) int {
	n := 0
	for _, k := range []string{"link_count", "edge_count", "relation_count", "relationship_count"} {
		if v, ok := digest[k].(int); ok {
			n += v
		}
	}
	return n
}

// ═══════════════════════════ 6. 经营合规数据 ═══════════════════════════

// 经营合规数据聚合 4 个 API（ParamKey 各异）
var complianceSections = []phase6Section{
	{Key: "admin_license", Pcode: "E_007_1101", Title: "行政许可", ParamKey: "entName"},
	{Key: "inspections", Pcode: "E_007_1601", Title: "抽查检查", ParamKey: "entName"},
	{Key: "qualifications", Pcode: "E_011_0202", Title: "资质证书", ParamKey: "name"},
	{Key: "import_export_credit", Pcode: "E_011_0801", Title: "进出口信用", ParamKey: "name"},
}

// OperationalCompliance 经营合规数据：并行聚合 4 个合规接口
func (s *Services) OperationalCompliance(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	entity, results, okCount, skippedCount, elapsed := runPhase6Aggregate(s, ctx, complianceSections, entName, companyID, 10, buildComplianceDigest)
	return buildPhase6Output("operational_compliance", entity, results, okCount, skippedCount, len(complianceSections), elapsed)
}

// buildComplianceDigest 经营合规各维度摘要
func buildComplianceDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	digest["record_count"] = count
	digest["has_data"] = hasAnyData(parsed)

	switch key {
	case "admin_license":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["license_count"] = len(list)
			}
		}
	case "inspections":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["inspection_count"] = len(list)
			}
		}
	case "qualifications":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			if list, ok := data["list"].([]any); ok {
				digest["cert_count"] = len(list)
				// 提取资质名称列表
				names := make([]string, 0, len(list))
				for _, c := range list {
					if cm, ok := c.(map[string]any); ok {
						if n, ok := cm["certName"].(string); ok && n != "" {
							names = append(names, n)
						} else if n, ok := cm["name"].(string); ok && n != "" {
							names = append(names, n)
						}
					}
				}
				if len(names) > 0 {
					digest["cert_names"] = names
				}
			}
		}
	case "import_export_credit":
		var data map[string]any
		if d, ok := parsed["data"].(map[string]any); ok {
			data = d
		}
		if data != nil {
			for _, f := range []string{"creditLevel", "customsCode", "validDate"} {
				if v, ok := data[f]; ok {
					digest[f] = v
				}
			}
		}
	}

	digest["summary"] = fmt.Sprintf("%s: %d 条记录", key, count)
	return digest
}

// ═══════════════════════════ 7. 司法深度排查 ═══════════════════════════

// 司法明细接口以【记录 id】为唯一主体入参（PDF 必填 id）：企业名/企业 id 直接传入
// 会被网关以 code=101 参数错误拒绝（2026-09-17 线上实测，6 节全灭的直接原因）。
// 正确链路：先查列表拿记录 id，再按记录类型路由明细——
//
//	司法协助列表（entName）→ 记录 id + typeState → 冻结/失效/续行/解除冻结/股东变更 明细
//	破产重整查询（name）    → 案件 id            → 破产重整案件明细
const (
	judicialAssistListPcode = "E_007_2301"
	bankruptcyListPcode     = "E_003_1811"
	// 列表单页上限：typeState 与记录 id 的来源；超量时提示改用 keeprisk_query 翻页
	judicialListPageSize = 50
	// 每类型明细抽样上限：列表已含全量记录概要，明细仅补充字段，避免重度企业打爆计费
	judicialDetailMaxPerType = 3
)

// judicialDetailSections 明细节稳定输出顺序（无记录的节输出 status=no_data，保持绿色语义）
var judicialDetailSections = []struct {
	Key   string
	Pcode string
	Title string
}{
	{"judicial_freeze", "E_007_2401", "司法冻结"},
	{"judicial_freeze_expiry", "E_007_2501", "司法冻结失效"},
	{"judicial_continuation", "E_007_2601", "司法协助续行"},
	{"judicial_unfreeze", "E_007_2701", "解除冻结"},
	{"judicial_shareholder_change", "E_007_2801", "司法协助股东变更"},
}

// judicialDetailRoute 按 2301 记录的 typeState 路由明细接口。
// 顺序敏感：「解除冻结」「冻结失效」都含「冻结」子串，必须先匹配特例再兜底「冻结」。
// 实测（2026-09-17）：冻结记录 id 打到解除冻结接口返回 110——记录 id 仅在
// 类型匹配的明细接口上有数据，路由错误会表现为"该类型无记录"的假阴性。
func judicialDetailRoute(typeState string) (pcode, key string, ok bool) {
	s := strings.TrimSpace(typeState)
	switch {
	case s == "":
		return "", "", false
	case strings.Contains(s, "失效"):
		return "E_007_2501", "judicial_freeze_expiry", true
	case strings.Contains(s, "解除"):
		return "E_007_2701", "judicial_unfreeze", true
	case strings.Contains(s, "变更"):
		return "E_007_2801", "judicial_shareholder_change", true
	case strings.Contains(s, "续"):
		return "E_007_2601", "judicial_continuation", true
	case strings.Contains(s, "冻结"):
		return "E_007_2401", "judicial_freeze", true
	default:
		// 未知 typeState：不猜路由（错路由=假阴性），记录保留在司法协助概览节
		return "", "", false
	}
}

// lookupEntNameByCompanyID 通过工商基本信息（E_007_0501）反查企业名称。
// 供仅支持 entName 主体的聚合节在只拿到 company_id 时补齐主体（+1 次网关调用）。
func (s *Services) lookupEntNameByCompanyID(ctx context.Context, companyID string) string {
	raw := s.callQuery(ctx, "E_007_0501", map[string]any{"companyId": companyID})
	parsed, ok := jsonRaw(raw).(map[string]any)
	if !ok {
		return ""
	}
	if errStr, ok := parsed["error"].(string); ok && errStr != "" {
		return ""
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		if v, _ := data["entName"].(string); v != "" {
			return v
		}
	}
	if v, _ := parsed["entName"].(string); v != "" {
		return v
	}
	return ""
}

// gatewayListJSON 从聚合层网关响应中提取 (data.list, data.total, ok)。
// ok=false 表示网关报错或响应不可解析（区别于"成功但无记录"）。
func gatewayListJSON(raw string) ([]any, int, bool) {
	parsed, ok := jsonRaw(raw).(map[string]any)
	if !ok {
		return nil, 0, false
	}
	if errStr, ok := parsed["error"].(string); ok && errStr != "" {
		return nil, 0, false
	}
	if code := codeOf(parsed["code"]); code != 100 && code != 110 {
		return nil, 0, false
	}
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		return nil, 0, true // code=110：查询成功无记录
	}
	total, _ := anyToInt(data["total"])
	lst, _ := data["list"].([]any)
	return lst, total, true
}

// JudicialDeepDive 司法深度排查：列表 → 按记录类型路由明细。
//
// 无司法记录时各节输出 status=no_data 的绿色结论（不是 error）；
// 网关错误逐节透出 message，全错时 overall_risk=unknown，防止把故障当"无风险"。
func (s *Services) JudicialDeepDive(ctx context.Context, entName, companyID string) string {
	start := time.Now()
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	apiCalls := 0
	resolved := false
	if entName == "" {
		// 列表接口以企业名为主体；仅传 company_id 时先反查企业名（+1 次调用）
		entName = s.lookupEntNameByCompanyID(ctx, companyID)
		apiCalls++
		resolved = true
		if entName == "" {
			return mustJSON(map[string]any{
				"error":  "company_id 未能解析出企业名称（工商照面无记录）",
				"hint":   "请改用 ent_name，或先用 resolve_entity 确认主体",
				"entity": companyID,
			})
		}
	}

	sections := make([]map[string]any, 0, 7)
	totalRecords := 0
	redSections := 0
	errorCount := 0

	// 1) 司法协助列表：记录 id 与 typeState 的唯一来源
	listRaw := s.callQuery(ctx, judicialAssistListPcode, map[string]any{
		"entName":  entName,
		"pageNo":   1,
		"pageSize": judicialListPageSize,
	})
	apiCalls++
	records, listTotal, listOK := gatewayListJSON(listRaw)
	assistCount := listTotal
	if assistCount == 0 {
		assistCount = len(records)
	}
	if !listOK {
		errorCount++
		sections = append(sections, map[string]any{
			"key": "judicial_assistance", "title": "司法协助列表", "status": "error",
			"error": "司法协助列表查询失败：" + gatewayErrText(listRaw) +
				"；明细接口依赖本列表的记录 id，列表失败时无法获取明细",
		})
	} else {
		totalRecords += assistCount
		section := map[string]any{
			"key": "judicial_assistance", "title": "司法协助列表",
			"status": "no_data", "record_count": 0,
		}
		if len(records) > 0 {
			section["status"] = "ok"
			section["record_count"] = assistCount
			section["records"] = records
			redSections++
			if listTotal > len(records) {
				section["list_truncated"] = true
				section["_note"] = fmt.Sprintf("共 %d 条记录，仅返回前 %d 条；翻页可用 keeprisk_query 查询司法协助接口", listTotal, len(records))
			}
		}
		sections = append(sections, section)

		// 2) 按 typeState 分组路由明细（每类型抽样，列表已含全量概要）
		grouped := map[string][]string{} // detail key → ids
		for _, r := range records {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			id, _ := rm["id"].(string)
			ts, _ := rm["typeState"].(string)
			if _, key, ok := judicialDetailRoute(ts); ok && id != "" {
				grouped[key] = append(grouped[key], id)
			}
		}
		for _, ds := range judicialDetailSections {
			ids := grouped[ds.Key]
			if len(ids) == 0 {
				sections = append(sections, map[string]any{
					"key": ds.Key, "title": ds.Title, "status": "no_data", "record_count": 0,
				})
				continue
			}
			sample := ids
			if len(sample) > judicialDetailMaxPerType {
				sample = sample[:judicialDetailMaxPerType]
			}
			details := make([]any, 0, len(sample))
			detailErr := ""
			for _, id := range sample {
				raw := s.callQuery(ctx, ds.Pcode, map[string]any{"id": id})
				apiCalls++
				lst, _, ok := gatewayListJSON(raw)
				if !ok {
					detailErr = gatewayErrText(raw)
					break
				}
				details = append(details, lst...)
			}
			sec := map[string]any{
				"key": ds.Key, "title": ds.Title, "status": "ok",
				"record_count": len(ids), "details": details,
				"detail_sampled": len(sample),
			}
			if detailErr != "" {
				// 记录概要仍在（列表已证实存在），但明细字段获取失败，不能静默当无记录
				sec["status"] = "error"
				sec["error"] = "明细查询失败：" + detailErr
				errorCount++
			}
			redSections++
			sections = append(sections, sec)
		}
	}

	// 3) 破产重整：列表（name）→ 案件明细（id）
	bRaw := s.callQuery(ctx, bankruptcyListPcode, map[string]any{
		"name": entName, "pageNo": 1, "pageSize": 10,
	})
	apiCalls++
	bRecords, bTotal, bOK := gatewayListJSON(bRaw)
	bCount := bTotal
	if bCount == 0 {
		bCount = len(bRecords)
	}
	bankSection := map[string]any{
		"key": "bankruptcy_detail", "title": "破产重整", "status": "no_data", "record_count": 0,
	}
	switch {
	case !bOK:
		bankSection["status"] = "error"
		bankSection["error"] = "破产重整列表查询失败：" + gatewayErrText(bRaw)
		errorCount++
	case len(bRecords) > 0:
		bankSection["status"] = "ok"
		bankSection["record_count"] = bCount
		bankSection["records"] = bRecords
		redSections++
		totalRecords += bCount
		// 案件明细补充字段（抽样第一条；多案件时概要已足够定位）
		if bm, ok := bRecords[0].(map[string]any); ok {
			if id, _ := bm["id"].(string); id != "" {
				dRaw := s.callQuery(ctx, "E_003_1821", map[string]any{"id": id})
				apiCalls++
				dList, _, dOK := gatewayListJSON(dRaw)
				if dOK && len(dList) > 0 {
					bankSection["detail"] = dList[0]
				} else if !dOK {
					bankSection["detail_error"] = gatewayErrText(dRaw)
				}
			}
		}
	}
	sections = append(sections, bankSection)

	summary := map[string]any{
		"entity":   entName,
		"sections": sections,
		// 记录总数以列表口径为准（明细是按 id 的字段补充，不代表记录数）
		"total_judicial_records": totalRecords,
		"red_flag_count":         redSections,
		"error_count":            errorCount,
		"api_calls":              apiCalls,
		"elapsed_ms":             time.Since(start).Milliseconds(),
		"_agent_meta":            agentMeta(entName),
		// 2026-09-20：overall_risk 只覆盖司法协助+破产重整两族接口，
		// 不含法院公告/开庭公告/裁判文书/司法拍卖等维度——字段名带"overall"曾诱导
		// 模型当作全口径结论（实测与本工具 low 并存的是 snapshot red）。
		// 显式声明覆盖面，全口径司法结论请用 enterprise_due_diligence_snapshot。
		"scope":      []string{"judicial_assist", "bankruptcy"},
		"scope_note": "本结论仅覆盖司法协助与破产重整维度；法院公告/开庭公告/裁判文书/司法拍卖等司法维度不在此列，全口径请用 enterprise_due_diligence_snapshot(include_judicial=true)",
	}
	switch {
	case totalRecords > 0:
		summary["overall_risk"] = "high"
	case errorCount > 0:
		// 故障≠无风险：存在未完成的排查节，禁止当绿色结论使用
		summary["overall_risk"] = "unknown"
		summary["risk_note"] = "部分节网关报错，无法确认是否无记录；请勿将本结果当作无风险结论，可稍后重试失败节"
	default:
		summary["overall_risk"] = "low"
		summary["risk_note"] = "司法协助与破产重整均无记录（列表接口 code=110，查询成功）"
	}
	if companyID != "" {
		summary["company_id"] = companyID
	}
	if resolved {
		summary["ent_name_resolved"] = entName
	}
	return mustJSON(summary)
}

// gatewayErrText 从聚合层网关响应提取人可读错误文本（空响应兜底）。
// QueryImpl 错误形态 {"error":"api","code":101,"message":"code=101 参数错误（…）"}：
// 旧实现只透传 "api"，模型无从判断是参数问题、记录不存在还是服务故障。
func gatewayErrText(raw string) string {
	if parsed, ok := jsonRaw(raw).(map[string]any); ok {
		if m, ok := parsed["message"].(string); ok && m != "" {
			return m
		}
		if m, ok := parsed["error"].(string); ok && m != "" {
			return m
		}
	}
	if strings.TrimSpace(raw) == "" {
		return "网关无响应"
	}
	return raw
}

// ═══════════════════════════ 通用辅助函数 ═══════════════════════════

// buildGenericCountDigest 通用计数摘要（用于财务/市场等只需 record_count 的维度）。
//
// 2026-09-18 修正（飞友科技全量实测，A2 类误报 20 处）：
//   - has_data 原按 `record_count > 0` 判定，对图类（nodes/links）、角标类（扁平数值对象）、
//     比值类（assetLiRate 等）一律误报 false。现改为复用 hasAnyData 的证据判定。
//   - record_count 取接口声明的数据总量 total（口径见 extractCount 注释）；
//     当页条数与总量不同时另给 page_count，避免把单页读成全部
//     （实测飞友科技商标 total=345、当页 10 条）。
func buildGenericCountDigest(key string, parsed map[string]any) map[string]any {
	digest := map[string]any{}
	count := extractCount(parsed)
	// E_017_0171 财务健康分析：字段名为文档口径（2026-09-18 核对）——
	// totalSales=近五年营收增长率、totalTax=近五年税收增长率、
	// industryTotalSalesRateAvg/industryTotalTaxRateAvg=同行业平均增长率、
	// lastFive*RateAvgDegree=领先度（**文档明示"需乘 100"**）。
	// 实测第 22 条：totalSales/totalTax 装的是增长率序列，字面易误读为销售额/纳税额。
	if key == "financial_health" {
		if d, ok := parsed["data"].(map[string]any); ok {
			labels := map[string]string{
				"totalSales":                   "近五年营收增长率（非销售额）",
				"totalTax":                     "近五年税收增长率（非纳税额）",
				"industryTotalSalesRateAvg":    "营收同行业平均增长率",
				"industryTotalTaxRateAvg":      "税收同行业平均增长率",
				"lastFiveTotalSalesRateAvg":    "近五年营收平均增长率",
				"lastFiveTotalTaxRateAvg":      "近五年税收平均增长率",
				"lastFiveTotalSalesRateAvgDegree": "营收平均增长率领先度（文档：需乘 100）",
				"lastFiveTotalTaxRateAvgDegree":   "税收平均增长率领先度（文档：需乘 100）",
			}
			present := map[string]string{}
			for f, label := range labels {
				if _, ok := d[f]; ok {
					present[f] = label
				}
			}
			if len(present) > 0 {
				digest["field_labels"] = present
			}
		}
	}

	// 提取常见财务/市场字段
	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	}
	if data != nil {
		for _, f := range []string{"secucode", "secuabbr", "reportDate", "endDate", "changePct", "price", "volume"} {
			if v, ok := data[f]; ok {
				digest[f] = v
			}
		}
	}

	hd := hasAnyData(parsed)
	if count > 0 || !hd {
		digest["record_count"] = count
	}
	digest["has_data"] = hd
	if total, ok := extractTotal(parsed); ok {
		digest["total"] = total
	}
	pc := 0
	if v := extractPageCount(parsed); v > 0 && v != count {
		digest["page_count"] = v
		pc = v
	}
	digest["summary"] = digestSummary(key, count, pc, hd)
	return digest
}
