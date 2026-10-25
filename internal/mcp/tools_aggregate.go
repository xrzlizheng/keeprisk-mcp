package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolAdder 是 registerTools 内部 add 闭包的类型签名，用于将工具注册逻辑拆分到多个文件。
type toolAdder func(name, desc string, annotations *mcp.ToolAnnotations, schema map[string]any, handler func(context.Context, map[string]any) (string, error))

// registerAggregateTools 注册所有并行聚合类工具（Phase 4/5/6 聚合工具 + 上市公司/供应链/招商等）。
// 从 server.go 的 registerTools 拆分而来，减少单函数行数。
func registerAggregateTools(add toolAdder, svc *Services, openWorld, readOnly *mcp.ToolAnnotations) {
	// listed_company_profile — 上市公司画像：并行聚合 8 个核心上市信息接口
	add("listed_company_profile", "上市公司画像：并行聚合8个核心接口（概况+关键指标+股票行情+十大股东+十大流通股东+股本结构+主营业务+定量评级），每维度含结构化摘要。入参 ent_name 或 company_id 二选一，企业须为上市公司。", openWorld, schemaListedCompanyProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.ListedCompanyProfile(ctx, ent, cid), nil
	})

	// supply_chain_relations — 供应链关系：供应商/客户 + 产业链图谱 + 节点统计
	add("supply_chain_relations", "供应链关系：并行聚合2个接口（供应商与客户+企业产业链图谱），回答'这家企业的上下游是谁'。产业链节点统计需 chain_name 主体，请用 industry_chain_profile。入参 ent_name 或 company_id 二选一。", openWorld, schemaSupplyChainRelations(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.SupplyChainRelations(ctx, ent, cid), nil
	})

	// investment_readiness_score — 投资准备度评分：投资意愿 + 综合评分 + 数据角标
	add("investment_readiness_score", "投资准备度评分：并行聚合4个接口（投资意愿评分+投资意愿分析+企业综合评分+数据角标），面向招商场景回答'这家企业值不值得招'。入参 ent_name 或 company_id 二选一；可选 chain_name（+province/city）——投资意愿两节为产业链维度接口，不传 chain_name 时自动跳过该两节（0 网关调用）。", openWorld, schemaInvestmentReadiness(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		chain, _ := args["chain_name"].(string)
		prov, _ := args["province"].(string)
		ct, _ := args["city"].(string)
		return svc.InvestmentReadinessScore(ctx, ent, cid, chain, prov, ct), nil
	})

	// industry_chain_profile — 产业链画像：并行聚合 4 个产业链分析接口
	add("industry_chain_profile", "产业链画像：并行聚合4个接口（产业链数据分析+地区统计+节点统计+财务数据），回答'这条产业链的全貌是什么'。入参 chain_name（产业链名称，必填），可选 province/city 缩小范围。", openWorld, schemaIndustryChainProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		chain, _ := args["chain_name"].(string)
		province, _ := args["province"].(string)
		city, _ := args["city"].(string)
		return svc.IndustryChainProfile(ctx, chain, province, city), nil
	})

	// estimate_billing — 本地计费估算（0 次网关调用）
	add("estimate_billing", "本地计费估算：不调用网关，根据工具名称和参数估算 API 调用次数。用于调用前成本预估。tool 为工具名称，params 为工具参数（可选，用于精确估算如 sections 数量）。", readOnly, schemaEstimateBilling(), func(ctx context.Context, args map[string]any) (string, error) {
		tool, _ := args["tool"].(string)
		var params map[string]any
		if raw, ok := args["params"]; ok {
			params, _ = raw.(map[string]any)
		}
		if params == nil {
			params = map[string]any{}
		}
		return svc.EstimateBilling(tool, params), nil
	})

	// enterprise_extended_profile — 企业全景画像：并行聚合 9 个工商/股权/标签接口
	add("enterprise_extended_profile", "企业全景画像：并行聚合9个接口（工商基本信息+股东+主要人员+企业标签+最终受益人+实际控制权+股权穿透+股权出质+数据角标），比 basic_profile 更深入。入参 ent_name 或 company_id 二选一。", openWorld, schemaEnterpriseExtendedProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.EnterpriseExtendedProfile(ctx, ent, cid), nil
	})

	// ─────────── Phase 5 聚合工具 ───────────

	// listed_company_financials — 上市公司财务数据：并行聚合 13 个财务接口
	add("listed_company_financials", "上市公司财务数据：并行聚合13个接口（资产负债表+年度财务+利润分配+单季现金流量表+单季利润表+单季度财务+报告期财务+现金流量表+财务比率+分红转增+分红配股+募资统计+增发数据），覆盖财务报表全貌。入参 ent_name 或 company_id 二选一，企业须为上市公司。", openWorld, schemaListedCompanyFinancials(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.ListedCompanyFinancials(ctx, ent, cid), nil
	})

	// listed_company_market — 上市公司市场数据：并行聚合 17 个市场/交易接口
	add("listed_company_market", "上市公司市场数据：并行聚合15个接口（股权质押明细+限售解禁+停牌历史+停牌实时+股东户数+机构持股+股东人数与股价+融资买入+融券卖出+融资融券余额+大宗交易+龙虎榜明细+龙虎榜席位+机构席位+机构席位交易），覆盖市场交易与机构行为。入参 ent_name 或 company_id 二选一，企业须为上市公司。", openWorld, schemaListedCompanyMarket(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.ListedCompanyMarket(ctx, ent, cid), nil
	})

	// annual_report_investments — 企业年报与投资：并行聚合 4 个接口
	add("annual_report_investments", "企业年报与投资：并行聚合4个接口（年报+年度财务+年度财务指标+对外投资V2），回答'这家企业的年报和对外投资情况'。入参 ent_name 或 company_id 二选一，可选 year 指定财务报告期（不传用默认期，响应会回告实际期次）。", openWorld, schemaAnnualReportInvestments(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		year, _ := args["year"].(string)
		return svc.AnnualReportInvestments(ctx, ent, cid, year), nil
	})

	// intellectual_property_portfolio — 知识产权全景：并行聚合 5 个接口
	add("intellectual_property_portfolio", "知识产权全景：并行聚合5个接口（专利+软件著作权+作品著作权+域名+商标），回答'这家企业的知识产权全貌'。入参 ent_name 或 company_id 二选一。", openWorld, schemaIntellectualPropertyPortfolio(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.IntellectualPropertyPortfolio(ctx, ent, cid), nil
	})

	// enterprise_graph_relations — 企业关联图谱：并行聚合 4 个接口
	add("enterprise_graph_relations", "企业关联图谱：并行聚合4个接口（企业图谱+股权穿透股东节点+股权穿透对外投资节点+实质关联图谱），回答'这家企业的关联关系'。默认 digest_only=true 只返回节点/边计数摘要（完整图可达 48KB+）；需要原始 nodes/links 时显式传 digest_only=false。入参 ent_name 或 company_id 二选一。", openWorld, schemaEnterpriseGraphRelations(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		// 默认 digest_only=true：见 schema 注释（48KB→2KB，token 主热点）
		do, has := args["digest_only"].(bool)
		if !has {
			do = true
		}
		return svc.EnterpriseGraphRelations(ctx, ent, cid, do), nil
	})

	// operational_compliance — 经营合规：并行聚合 4 个接口
	add("operational_compliance", "经营合规：并行聚合4个接口（行政许可+抽查检查+资质证书+进出口信用），回答'这家企业的经营合规状态'。入参 ent_name 或 company_id 二选一。", openWorld, schemaOperationalCompliance(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.OperationalCompliance(ctx, ent, cid), nil
	})

	// judicial_deep_dive — 司法深度：列表 → 按记录类型路由明细
	add("judicial_deep_dive", "司法深度：先取司法协助与破产重整记录列表，再按记录 id 和类型路由明细（司法冻结/冻结失效/续行/解除冻结/股东变更/破产案件详情）。企业无司法记录时返回绿色无记录结论（非错误）。入参 ent_name 或 company_id 二选一。", openWorld, schemaJudicialDeepDive(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.JudicialDeepDive(ctx, ent, cid), nil
	})

	// ─────────── Phase 6 聚合工具 ───────────

	add("data_badge_full", "全量数据角标：并行聚合4个角标接口（司法角标+企业角标+经营角标+知识产权角标），比 credit_score 中单一角标更全面。入参 ent_name 或 company_id 二选一。", openWorld, schemaDataBadgeFull(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.DataBadgeFull(ctx, ent, cid), nil
	})

	add("enterprise_business_detail", "企业经营详情：并行聚合8个接口（分支机构+变更信息+标讯+行政许可(信用中国)+知识产权出质+行政处罚(信用中国)+实际股东+注销信息），补充尽调/合规场景。入参 ent_name 或 company_id 二选一。", openWorld, schemaEnterpriseBusinessDetail(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.EnterpriseBusinessDetail(ctx, ent, cid), nil
	})

	add("enterprise_deep_profile", "企业深度画像补充：并行聚合6个接口（注册地核验+企业坐标+曾用名+联系方式+联系方式V2+企业关联信息），补充 extended_profile 未覆盖的工商附加维度。董监高/人员任职请用 person_risk_profile。入参 ent_name 或 company_id 二选一。", openWorld, schemaEnterpriseDeepProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.EnterpriseDeepProfile(ctx, ent, cid), nil
	})

	add("listed_company_extended", "上市公司扩展信息：并行聚合10个接口（银保监处罚+高管数据(董事会/监事会/其他)+行业分析(比较/分析/财务)+龙虎榜明细V2+A股发行信息+行业代码），补充 listed_company_profile/financials/market 未覆盖的上市维度。入参 ent_name 或 company_id 二选一，须为上市公司。", openWorld, schemaListedCompanyExtended(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.ListedCompanyExtended(ctx, ent, cid), nil
	})

	add("investment_analysis_suite", "招商宝分析套件：并行聚合7个接口（招聘分析+专利分析+批量查询+财务健康+投资行为+股东分析+研发实力），面向招商场景深度评估企业综合实力。入参 ent_name 或 company_id 二选一。", openWorld, schemaInvestmentAnalysisSuite(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.InvestmentAnalysisSuite(ctx, ent, cid), nil
	})

	add("enterprise_development", "企业发展：并行聚合5个接口（核心团队+融资历史+竞品信息+企业业务+发展历程），回答'这家企业的成长轨迹'。入参 ent_name 或 company_id 二选一。", openWorld, schemaEnterpriseDevelopment(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.EnterpriseDevelopment(ctx, ent, cid), nil
	})

	add("investment_event_profile", "投资事件画像：并行聚合5个接口（投资事件+投资趋势+合作机构排名+合作机构+投资机构详情），回答'这家企业的投资行为与合作伙伴'。入参 ent_name 或 company_id 二选一。", openWorld, schemaInvestmentEventProfile(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		return svc.InvestmentEventProfile(ctx, ent, cid), nil
	})

	add("enterprise_evaluation_report", "企业评价报告：并行聚合5个接口（创始团队+创新技术竞争力+行业饱和度+行业垄断程度+行业成长情况），面向招商/投资评估。入参 ent_name 或 company_id 二选一。行业维度3节（饱和度/垄断/成长）需 province（可选 city/county/economic_region 细化），未提供地区时跳过。", openWorld, schemaEnterpriseEvaluationReport(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		prov, _ := args["province"].(string)
		ct, _ := args["city"].(string)
		co, _ := args["county"].(string)
		er, _ := args["economic_region"].(string)
		return svc.EnterpriseEvaluationReportWithRegion(ctx, ent, cid, prov, ct, co, er), nil
	})

	add("annual_report_detail", "年报详情级联：先自动获取最新年报ID，再并行查7个子表（年报变更+股东股权变更+对外投资+股东及出资信息+社保+网站+对外担保），回答'这家企业年报的详细子表数据'。可传入 report_id 跳过列表查询节省1次调用。入参 ent_name 或 company_id 二选一。", openWorld, schemaAnnualReportDetail(), func(ctx context.Context, args map[string]any) (string, error) {
		ent, _ := args["ent_name"].(string)
		cid, _ := args["company_id"].(string)
		rid, _ := args["report_id"].(string)
		return svc.AnnualReportDetail(ctx, ent, cid, rid), nil
	})

	add("industrial_park_search", "产业园区搜索：按园区名称并行聚合3个接口（园区列表+园区企业列表+园区企业分布），按省份/城市调园区筛选，回答'这个区域有哪些产业园区/这个园区有哪些企业'。park_name、park_id、province/city 至少提供一个。园区详情可通过 keeprisk_query 按 park_id 直调。", openWorld, schemaIndustrialParkSearch(), func(ctx context.Context, args map[string]any) (string, error) {
		pn, _ := args["park_name"].(string)
		pid, _ := args["park_id"].(string)
		prov, _ := args["province"].(string)
		ct, _ := args["city"].(string)
		return svc.IndustrialParkSearchWithID(ctx, pn, pid, prov, ct), nil
	})
}
