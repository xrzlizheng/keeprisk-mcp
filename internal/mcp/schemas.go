package mcp

import "strings"

// schemas.go — 所有 MCP 工具的 JSON Schema 定义 + resolve_entity 实现
//
// 设计原则：
//   1. 每个工具对应一个函数，返回 map[string]any（标准 JSON Schema draft 2020-12）
//   2. 必填参数放入 required 数组；可选参数列在 properties 中但不放入 required
//   3. 对于参数极多的筛选工具（search_enterprises / search_chain_enterprises），
//      仅列出核心参数，additionalProperties 保持 true 允许透传其余参数
//   4. 描述使用中文，与 serverInstructions 和 tool description 保持一致
//
// SDK 兼容性：go-sdk v0.8.0 的 Tool.InputSchema 为 any 类型，
// 当 AddTool[map[string]any, any] 检测到非 nil InputSchema 时直接使用，不做推断。

// ─────────────────────────── 辅助构建器 ───────────────────────────

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func strEnum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func intMinMax(desc string, min, max int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": min, "maximum": max}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func arrItems(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}

func objProp(desc string, props map[string]any, additional bool) map[string]any {
	// nil map 序列化成 "properties": null，违反 JSON Schema 元schema（properties 出现时
	// 须为 object），下游 agent 平台对 function 定义做严格校验会直接拒绝该工具
	// （实测报错 null is not of type "object"）。此处兜底为空对象。
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{
		"type":                 "object",
		"description":          desc,
		"properties":           props,
		"additionalProperties": additional,
	}
}

func schemaOf(required []string, props map[string]any) map[string]any {
	return schemaOfExt(required, props, nil, nil)
}

func schemaOfExt(required []string, props map[string]any, additionalProperties *bool, anyOf []map[string]any) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if additionalProperties != nil {
		s["additionalProperties"] = *additionalProperties
	}
	if len(anyOf) > 0 {
		items := make([]any, 0, len(anyOf))
		for _, item := range anyOf {
			items = append(items, item)
		}
		s["anyOf"] = items
	}
	return s
}

func selectorAnyOf(keys ...string) []map[string]any {
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"required": []string{key}})
	}
	return out
}

func entitySelectorSchema(extraProps map[string]any) map[string]any {
	props := map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一）"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
	}
	for k, v := range extraProps {
		props[k] = v
	}
	return schemaOfExt(nil, props, nil, selectorAnyOf("ent_name", "company_id"))
}

func coordItemSchema() map[string]any {
	return objProp("", map[string]any{
		"lon": strProp("经度"),
		"lat": strProp("纬度"),
	}, false)
}

func pointProp(desc string) map[string]any {
	return objProp(desc, map[string]any{
		"type":       intProp("类型：1=圆形距离筛选，2=多边形范围筛选"),
		"km":         intProp("半径公里数，仅 type=1 时有效"),
		"lon":        strProp("经度，仅 type=1 时有效"),
		"lat":        strProp("纬度，仅 type=1 时有效"),
		"local":      arrItems("多边形坐标点集合，仅 type=2 时有效，至少 3 个点", coordItemSchema()),
		"sortOrder":  strEnum("距离排序", "asc", "desc"),
	}, false)
}

func regionItemSchema(pr, ci, co string) map[string]any {
	return objProp("", map[string]any{
		pr: strProp("省份"),
		ci: strProp("城市"),
		co: strProp("区/县"),
		"economicRegion": strProp("经济区划"),
	}, false)
}

func industryLevelItemSchema() map[string]any {
	return objProp("", map[string]any{
		"oneLevel":  strProp("一级行业"),
		"twoLevel":  strProp("二级行业"),
		"treeLevel": strProp("三级行业"),
		"fourLevel": strProp("四级行业"),
	}, false)
}

// schemaEmptyObject 无参工具的 MCP input schema（go-sdk 禁止 nil）
func schemaEmptyObject() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// ─────────────────────────── 工具 Schema 定义 ───────────────────────────

// keeprisk_query — 已知 pcode 直调网关
func schemaKeepriskQuery() map[string]any {
	return schemaOf(
		[]string{"pcode"},
		map[string]any{
			"pcode": strProp("接口编码或接口名称（search_keeprisk_apis 返回的 title，如「院校信息」）。多版本 pcode 自动取最新版。"),
			"params": objProp("业务参数字典。主体字段名因接口而异（entName/name/text/companyName/keyword/chainHis），建议先用 get_keeprisk_api 确认。", nil, true),
		},
	)
}

// enterprise_due_diligence_snapshot — 尽调快照（26 维度）
func schemaDueDiligence() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":         strProp("企业名称（ent_name 或 company_id 二选一）"),
		"company_id":       strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
		"sections":         strProp("逗号分隔的维度 key 列表，默认全开 26 节。候选：basic,shareholders,key_personnel,court_filing,zhongben,judgment,enforced,dishonest,restraint,serious_dishonesty,admin_penalty,abnormal_operation,serious_illegal,liquidation,tax_violation,tax_arrears,court_announcement,judicial_auction,bankruptcy,court_hearing,judicial_assist,equity_pledge,chattel_mortgage,tax_rating,public_sentiment,beneficial_owner"),
		"include_judicial": boolProp("是否包含司法维度（默认 true）。设为 false 可节省计费，经营风险维度不受此开关影响。"),
		"max_concurrency":  intMinMax("并行请求数（默认 4，范围 1-9）", 1, 9),
		"digest_only":      boolProp("仅返回结构化摘要（默认 false）。设为 true 省略原始数据，大幅减少 token。"),
	})
}

// enterprise_basic_profile — 工商照面快捷查询
func schemaBasicProfile() map[string]any {
	return entitySelectorSchema(nil)
}

// enterprise_credit_score — 企业信用评分（低成本门卫工具）
func schemaCreditScore() map[string]any {
	return entitySelectorSchema(nil)
}

// listed_company_profile — 上市公司画像
func schemaListedCompanyProfile() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一），须为上市公司"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
	})
}

// supply_chain_relations — 供应链关系
func schemaSupplyChainRelations() map[string]any {
	return entitySelectorSchema(nil)
}

// investment_readiness_score — 投资准备度评分
//
// 2026-09-20 增补 chain_name/province/city：投资意愿评分/分析（E_017_0161/0231）为
// 产业链维度接口（文档必填 chainName），不传时两节直接 skipped（0 网关调用），
// 而不是像旧版那样对每个主体白付 2 次注定 101 的调用。
func schemaInvestmentReadiness() map[string]any {
	return entitySelectorSchema(map[string]any{
		"chain_name": strProp("产业链名称（可选，如「人工智能产业链」，有效名称可用 search_chains 查询）。提供后才会执行投资意愿评分/分析两节（该两节为产业链维度接口，文档必填 chainName）"),
		"province":   strProp("省份（可选，配合 chain_name 传入投资意愿接口的 regionMap）"),
		"city":       strProp("城市（可选，配合 chain_name 传入投资意愿接口的 regionMap）"),
	})
}

// industry_chain_profile — 产业链画像
func schemaIndustryChainProfile() map[string]any {
	return schemaOf(
		[]string{"chain_name"},
		map[string]any{
			"chain_name": strProp("产业链名称（必填），如\"人工智能产业链\"、\"新能源汽车产业链\""),
			"province":   strProp("省份过滤（可选），如\"广东\"、\"浙江\""),
			"city":       strProp("城市过滤（可选），如\"深圳\"、\"杭州\""),
		})
}

// estimate_billing — 计费估算
func schemaEstimateBilling() map[string]any {
	return schemaOf(
		[]string{"tool"},
		map[string]any{
			"tool":   strProp("工具名称，如 enterprise_due_diligence_snapshot、enterprise_credit_score 等"),
			"params": objProp("工具参数（可选），用于精确估算（如 sections、ent_names 等）", nil, true),
		})
}

// enterprise_extended_profile — 企业全景画像
func schemaEnterpriseExtendedProfile() map[string]any {
	return entitySelectorSchema(nil)
}

// listed_company_financials — 上市公司财务数据（13 维度）
func schemaListedCompanyFinancials() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一），须为上市公司"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
	})
}

// listed_company_market — 上市公司市场交易数据（17 维度）
func schemaListedCompanyMarket() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一），须为上市公司"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
	})
}

// annual_report_investments — 企业年报与对外投资
//
// 2026-09-18 增补 year：其中「企业财务年报」(E_007_0931) 的 year 为**必填**，
// 不传则稳定返回 code=110（实测：该节在聚合里恒为空，工具宣称的"年度财务"维度
// 实际从未取到数据）。给可选 year 后，不传走默认期、传了按指定期取。
func schemaAnnualReportInvestments() map[string]any {
	return entitySelectorSchema(map[string]any{
		"year": strProp("报告年份 YYYY（可选）。仅影响「企业财务年报 / 企业财务年报指标」两个子表；" +
			"不传时使用默认期（上游对财务年报要求 year 必填，缺省期可能不是最新报告期）"),
	})
}

// intellectual_property_portfolio — 知识产权组合
func schemaIntellectualPropertyPortfolio() map[string]any {
	return entitySelectorSchema(nil)
}

// enterprise_graph_relations — 企业关联图谱
//
// digest_only 默认 true：图类原始载荷极大（实测企业图谱节 143 节点+213 边 ≈36KB，
// 单次工具响应 48KB），默认只回 digest（节点/边计数）；需要全图时显式传 false。
func schemaEnterpriseGraphRelations() map[string]any {
	return entitySelectorSchema(map[string]any{
		"digest_only": boolProp("仅返回各图摘要（节点/边计数），省略原始 nodes/links 数组，大幅减少 token（实测 48KB→约2KB）。默认 true；需要完整图数据时传 false"),
	})
}

// operational_compliance — 经营合规数据
func schemaOperationalCompliance() map[string]any {
	return entitySelectorSchema(nil)
}

// judicial_deep_dive — 司法深度排查
func schemaJudicialDeepDive() map[string]any {
	return entitySelectorSchema(nil)
}

// ─── Phase 6 聚合工具 schema ───

func schemaDataBadgeFull() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaEnterpriseBusinessDetail() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaEnterpriseDeepProfile() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaListedCompanyExtended() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一），须为上市公司"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
	})
}

func schemaInvestmentAnalysisSuite() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaEnterpriseDevelopment() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaInvestmentEventProfile() map[string]any {
	return entitySelectorSchema(nil)
}

func schemaEnterpriseEvaluationReport() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一）"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
		"province":   strProp("省份（可选；提供后才会执行行业饱和度/垄断程度/成长情况 3 个地区维度接口）"),
		"city":       strProp("城市（可选；传入时需同时提供 province）"),
		"county":     strProp("区/县（可选；传入时需同时提供 province 和 city）"),
		"economic_region": strProp("经济区划（可选；传入时需同时提供 province）"),
	})
}

func schemaAnnualReportDetail() map[string]any {
	return entitySelectorSchema(map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一）"),
		"company_id": strProp("企业唯一 ID（ent_name 或 company_id 二选一）"),
		"report_id":  strProp("年报 ID（可选）。不传则自动获取最新年报 ID，传入可跳过列表查询节省 1 次调用。"),
	})
}

func schemaIndustrialParkSearch() map[string]any {
	props := map[string]any{
		"park_name": strProp("园区名称关键词（可选，与 park_id/province 至少提供一个）"),
		"park_id":   strProp("园区 ID（可选；已知 parkId 时可直查园区企业列表）"),
		"province":  strProp("省份筛选（可选），如\"广东\""),
		"city":      strProp("城市筛选（可选；传入时需同时提供 province）"),
	}
	return schemaOfExt(nil, props, nil, selectorAnyOf("park_name", "park_id", "province"))
}

// batch_query_companies — 批量工商核验
func schemaBatchQuery() map[string]any {
	return schemaOf(nil, map[string]any{
		"ent_names": arrItems("企业名称数组（最多 100 条）", map[string]any{"type": "string"}),
		"company_ids": arrItems("企业 ID 数组（最多 100 条）", map[string]any{"type": "string"}),
	})
}

// control_penetration — 股权穿透
func schemaControlPenetration() map[string]any {
	return schemaOf(
		[]string{"ent_name"},
		map[string]any{
			"ent_name": strProp("企业名称（必填），以该企业为起点查询股权关系"),
		},
	)
}

// actual_controller — 实际控制权
func schemaActualController() map[string]any {
	return schemaOf(
		[]string{"ent_name"},
		map[string]any{
			"ent_name":  strProp("企业名称（必填）：返回该企业控制的下游企业清单（direction=outbound）"),
			"page":      intProp("页码（默认 1）"),
			"page_size": intProp("每页条数（默认 10）"),
		},
	)
}

// search_chain_enterprises — 产业链企业筛选
// 参数极多（50+），仅列出核心参数，additionalProperties=true 透传其余
func schemaSearchChainEnterprises() map[string]any {
	return schemaOfExt(
		[]string{"chain"},
		map[string]any{
			"chain":             strProp("产业链名称（必填），如\"人工智能产业链\""),
			"node_code":         strProp("产业链节点编码"),
			"keyword":           strProp("企业名称模糊搜索"),
			"province":          strProp("省份"),
			"city":              strProp("城市"),
			"county":            strProp("区/县"),
			"company_status":    map[string]any{"description": "企业状态：可传单个中文状态字符串，或状态码整数数组", "anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}},
			"industry":          strProp("一级行业名称"),
			"credit_code":       strProp("统一社会信用代码（精确匹配）"),
			"economic_region":   strProp("经济区划"),
			"company_org_type":  arrItems("企业类型（整数码数组或字符串数组）", map[string]any{}),
			"capital_type":      arrItems("币种类型（1人民币/2美元/3其他）", map[string]any{"type": "integer"}),
			"total_sales_range": arrItems("营收区间码数组（1≥1000亿…14<-1000亿）", map[string]any{"type": "integer"}),
			"listed":            arrItems("上市状态码数组（1A股/2港股/…/10未上市）", map[string]any{"type": "integer"}),
			"technology_state":  arrItems("科技资质码数组（1高新企业/…/35技术创新示范）", map[string]any{"type": "integer"}),
			"exist_type":        arrItems("存在条件码数组（1电话/…/32破产重整）", map[string]any{"type": "integer"}),
			"finance":           arrItems("融资轮次码数组（1天使/种子轮/…/13未融资）", map[string]any{"type": "integer"}),
			"exclude_region_list": arrItems("排除地区对象数组 [{province,city,county,economicRegion}]", regionItemSchema("province", "city", "county")),
			"score_region_map":  objProp("投资意愿评分地区 {province,city,county,economicRegion}", map[string]any{"province": strProp("省份"), "city": strProp("城市"), "county": strProp("区/县"), "economicRegion": strProp("经济区划")}, false),
			"industry_levels":   arrItems("多级行业对象数组 [{oneLevel,twoLevel,treeLevel,fourLevel}]", industryLevelItemSchema()),
			"reg_capital":       arrItems("注册资金区间（万元）[{from,to}]", objProp("", nil, true)),
			"establish_times":   arrItems("成立日期区间 [{from,to}]（yyyy-MM-dd）", objProp("", nil, true)),
			"contributors_ins":  arrItems("参保人数区间 [{from,to}]", objProp("", nil, true)),
			"point":             pointProp("企业坐标筛选对象"),
			"field_sort":        intProp("排序字段（1注册资金/2成立时间/3营收区间/4税收区间/5综合评分/6科创评分/7招商评分/8对外投资数量）"),
			"sort_order":        strEnum("排序方向", "asc", "desc"),
			"page":              intProp("页码（默认 1）"),
			"page_size":         intProp("每页条数（默认 20，最大 50）"),
		},
		boolPtr(true),
		nil,
	)
}

// get_chain_nodes — 产业链节点树
func schemaGetChainNodes() map[string]any {
	return schemaOf(
		[]string{"chain"},
		map[string]any{
			"chain": strProp("产业链名称（必填），如\"人工智能产业链\""),
		},
	)
}

// search_chains — 产业链模糊搜索
func schemaSearchChains() map[string]any {
	return schemaOf([]string{"keyword"}, map[string]any{
		"keyword":   strProp("产业链名称关键词（必填）"),
		"data_type": intProp("数据类型（0通用/1项目定制，默认 0）"),
		"page":      intProp("页码（默认 1）"),
		"page_size": intProp("每页条数（默认 10）"),
	})
}

// search_enterprises — 企业筛选 V4
// 参数极多（60+），仅列出核心参数
func schemaSearchEnterprises() map[string]any {
	return schemaOfExt(nil, map[string]any{
		"keyword":    strProp("搜索关键词"),
		"search_in":  strProp("搜索类型：1企业名称/2统一社会信用代码/3注册号/4法人名称/5注册地址/6经营范围/7联系电话/8证券代码/9证券简称/10品牌产品/11商标（默认 6）"),
		"company_ids": arrItems("企业 ID 数组", map[string]any{"type": "string"}),
		"province":   strProp("省份"),
		"city":       strProp("城市"),
		"county":     strProp("区/县"),
		"industry":   strProp("一级行业名称"),
		"industry_levels": arrItems("多级行业对象数组 [{oneLevel,twoLevel,treeLevel,fourLevel}]", industryLevelItemSchema()),
		"company_status": map[string]any{"description": "企业状态：可传单个中文状态字符串，或状态码整数数组", "anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}},
		"high_quality_ent": arrItems("优质企业码数组（1区营收百强/2区纳税百强/3园区企业/4国企央企/5中国500强/…）", map[string]any{"type": "integer"}),
		"capital_type":     arrItems("币种类型码数组（1人民币/2美元/3其他/4疑似外资）", map[string]any{"type": "integer"}),
		"finance":          arrItems("融资轮次码数组", map[string]any{"type": "integer"}),
		"listed":           arrItems("上市状态码数组", map[string]any{"type": "integer"}),
		"technology":       arrItems("科技资质码数组（1高新企业/…/33绿色工厂）", map[string]any{"type": "integer"}),
		"exist_type":       arrItems("存在条件码数组", map[string]any{"type": "integer"}),
		"retained_profit_range": arrItems(`净利润范围（利润表口径，net income）。⚠️ 非资产负债表"留存收益"（retained earnings）；字段名 retained_profit 为上游接口历史命名，实际语义是净利润。`, map[string]any{"type": "integer"}),
		"reg_capital_garden": arrItems("注册资金区间（万元）[{from,to}]", objProp("", nil, true)),
		"establish_times":    arrItems("成立日期区间 [{from,to}]（yyyy-MM-dd）", objProp("", nil, true)),
		"point":             pointProp("企业坐标筛选对象"),
		"field_sort":         intProp("排序字段（1成立时间/2成立时间+注册资金/3注册资金/4综合评分/5荣誉发布日期）"),
		"sort_order":         strEnum("排序方向", "asc", "desc"),
		"page":               intProp("页码（默认 1）"),
		"page_size":          intProp("每页条数（默认 20，最大 50）"),
	}, boolPtr(true), nil)
}

// list_categories — 浏览接口目录
func schemaListCategories() map[string]any {
	return schemaOf(nil, map[string]any{
		"product_line": strProp("产品线名称（可选）。为空返回全部产品线概览+分类样本；非空返回该产品线下的分类。"),
	})
}

// search_keeprisk_apis — 搜索/匹配接口
func schemaSearchApis() map[string]any {
	return schemaOf(nil, map[string]any{
		"mode":         strEnum("模式：search（关键词搜索，默认）或 resolve（意图匹配）", "search", "resolve"),
		"query":        strProp("搜索关键词（mode=search 时使用）"),
		"intent":       strProp("自然语言意图描述（mode=resolve 时使用），如\"查企业失信记录\""),
		"category":     strProp("按分类过滤（mode=search 时可选）"),
		"product_line": strProp("按产品线过滤"),
		"limit":        intProp("返回条数上限（mode=search 时，默认 25）"),
	})
}

// get_keeprisk_api — 单个接口元数据
func schemaGetApi() map[string]any {
	return schemaOf(
		[]string{"pcode"},
		map[string]any{
			"pcode": strProp("接口编码或接口名称（search_keeprisk_apis 返回的 title）"),
		},
	)
}

// validate_keeprisk_params — 本地参数校验
func schemaValidateParams() map[string]any {
	return schemaOf(
		[]string{"pcode"},
		map[string]any{
			"pcode":  strProp("接口编码或接口名称（search_keeprisk_apis 返回的 title）"),
			"params": objProp("待校验的业务参数字典", nil, true),
		},
	)
}

// school_alumni_circle — 校友圈企业（校企域复合工具，双入口）
func schemaSchoolAlumniCircle() map[string]any {
	return schemaOf(
		nil, // 两个入口二选一，见 description
		map[string]any{
			"university_name": strProp("院校官方全称（如 中国科学技术大学、清华大学）。与 company_name 二选一。" +
				"优先传全称以避免额外解析开销；传入可识别的简称（如 中科大）时本工具会自动解析为全称（+1 次网关调用），解析失败会明确报错而非返回空结果"),
			"company_name": strProp("企业名称（与 university_name 二选一）。用于由其法人院校反查校友圈：" +
				"内部走「工商基本信息」取内部 id →「校企筛选V2」companyId 维度取人员学历 → 按院校筛校友圈企业（共 3 次网关调用）。" +
				"法人无学历数据时返回该公司有学历记录的人员列表，不返回空结果"),
			"province": strProp("省份（区划筛选锚点，如 安徽）。⚠️该接口的区划必须嵌套在 regionList 数组内才生效，" +
				"本工具会代为构造——请勿跳过本工具用平铺 province 直调（实测被上游静默忽略且照常计费）"),
			"city":   strProp("市（可选，如 合肥市）。须与 province 同传；仅传 city 会被上游忽略"),
			"county": strProp("区/县（可选，如 蜀山区）。须与 province 同传"),
			"page_size": intMinMax("每页条数（默认 100，最大 100）。上游支持 pageNo 分页；"+
				"单页结果为抽样，完整性以响应 upstream_total 为准", 1, 100),
			"page":      intMinMax("起始页码（默认 1）。上游「校企筛选V2」支持 pageNo 翻页；配 max_pages 可连续扫描多页", 1, 1000),
			"max_pages": intMinMax("连续扫描页数（默认 1，最大 10；每页 1 次网关调用、计费）", 1, 10),
			"include_stats": boolProp("是否附带该校校友企业地域/行业分布摘要（+1 次网关调用，默认 false）"),
		},
	)
}

func schemaFuzzySearch() map[string]any {
	return schemaOf(
		nil, // 必填在 handler 内校验（SDK 层 required 会硬拒、无法给业务指引；见 arghelp.go）
		map[string]any{
			"keyword": strProp("模糊关键词（必填）：企业名/统一社会信用代码/电话/人名等（自动路由）。也接受 ent_name/company_name/name 别名"),
			"region":  strProp("地区限定（可选）"),
			"limit":   intMinMax("返回条数（默认 5，1-20）", 1, 20),
			"fields":  strEnum("候选字段集：anchor=仅锚定字段（默认，entName/creditCode/法人/状态/地区，实测 16.7KB→约1.7KB）；full=完整字段（含 60+ 标签/排名）", "anchor", "full"),
		},
	)
}

// fieldsArg 读取通用 fields 参数（fuzzy_search 等的字段集开关，默认 anchor）
func fieldsArg(args map[string]any) string {
	if v, ok := args["fields"].(string); ok {
		return v
	}
	return "anchor"
}

func schemaCompanyQuickScan() map[string]any {
	return schemaOfExt(nil, map[string]any{
		"ent_name":    strProp("企业名称"),
		"company_id":  strProp("企业 ID"),
		"keyword":     strProp("关键词（无 ent_name/company_id 时使用，等同企业名）"),
		"depth":       strEnum("扫描深度：light(3维风险) / standard(默认) / deep(全量尽调digest)", "light", "standard", "deep"),
	}, nil, selectorAnyOf("ent_name", "company_id", "keyword"))
}

func schemaCompareEnterprises() map[string]any {
	return schemaOf(nil, map[string]any{
		"ent_names":    arrItems("企业名称列表（2-5 家）", map[string]any{"type": "string"}),
		"company_ids":  arrItems("企业 ID 列表（2-5 家）", map[string]any{"type": "string"}),
	})
}

func schemaPersonRiskProfile() map[string]any {
	return schemaOf(
		nil, // 必填在 handler 内校验（见 arghelp.go：SDK 层 required 会硬拒、无法给业务指引）
		map[string]any{
			"person_name": strProp("人员姓名（必填，如\"陈康康\"）。也接受 name 别名"),
			"ent_name":    strProp("企业名称（可选）：附加返回该公司任职名单，用于比对同名是否同一人；不参与风险记录过滤"),
		},
	)
}

func schemaEntityRiskTimeline() map[string]any {
	return schemaOf(nil, map[string]any{
		"ent_name":   strProp("企业名称（ent_name 或 company_id 二选一）"),
		"company_id": strProp("企业 ID"),
		"limit":      intMinMax("时间线条目上限（默认 20，最大 50）", 1, 50),
	})
}

// schemaResolveEntity — 主体锚定
//
// 注意：入口工具的"必填"不写进 JSON Schema 的 required——SDK 会在协议层硬拒
// （-32602 missing properties），无法返回带业务语义与示例的指引；改为在
// 字段描述标注 + handler 内做别名归一与富错误（见 arghelp.go）。
func schemaResolveEntity() map[string]any {
	return schemaOf(
		nil,
		map[string]any{
			"keyword": strProp("企业名称关键词（必填；全称/简称/曾用名均可），如\"华为技术\"。也接受 ent_name/company_name/name 别名"),
			"region":  strProp("地区限定（可选），如\"广东\"、\"深圳\"，缩小候选范围"),
			"limit":   intMinMax("返回候选数上限（默认 5，范围 1-20）", 1, 20),
		},
	)
}

// ─────────────────────────── resolve_entity 实现 ───────────────────────────

// buildResolveCandidates 从 E_007_1331 响应中提取结构化候选列表
//
// 响应结构（E_007_1331）：
//
//	{ "code": 100, "data": { "list": [{ "entName", "uid", "creditCode", "legalName", "province", "city", "regStatusCode", ... }], "total": N } }
func (s *Services) buildResolveCandidates(parsed map[string]any, keyword string, limit int) []map[string]any {
	var items []any

	// 尝试多种响应结构
	if data, ok := parsed["data"].(map[string]any); ok {
		if list, ok := data["list"].([]any); ok {
			items = list
		} else if items2, ok := data["items"].([]any); ok {
			items = items2
		} else if rows, ok := data["rows"].([]any); ok {
			items = rows
		}
	} else if list, ok := parsed["list"].([]any); ok {
		items = list
	}

	candidates := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if len(candidates) >= limit {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		candidate := map[string]any{}

		// 企业名称
		if v, ok := m["entName"].(string); ok && v != "" {
			candidate["ent_name"] = v
		}
		// 企业唯一 ID
		if v, ok := m["uid"].(string); ok && v != "" {
			candidate["company_id"] = v
		}
		// 统一社会信用代码
		if v, ok := m["creditCode"].(string); ok && v != "" {
			candidate["credit_code"] = v
		}
		// 法人
		if v, ok := m["legalName"].(string); ok && v != "" {
			candidate["legal_person"] = v
		}
		// 省市
		if v, ok := m["province"].(string); ok && v != "" {
			candidate["province"] = v
		}
		if v, ok := m["city"].(string); ok && v != "" {
			candidate["city"] = v
		}
		// 经营状态（码+中文）
		if v, ok := m["regStatusCode"].(string); ok && v != "" {
			candidate["reg_status_code"] = v
		}
		if v, ok := m["regStatus"].(string); ok && v != "" {
			candidate["reg_status"] = v
		}
		// 企业类型
		if v, ok := m["entType"].(string); ok && v != "" {
			candidate["ent_type"] = v
		}
		// 注册资金
		if v, ok := m["regCap"].(string); ok && v != "" {
			candidate["reg_capital"] = v
		}
		// 成立时间
		if v, ok := m["estiblishTime"].(string); ok && v != "" {
			candidate["establish_time"] = v
		}

		// 匹配提示：精确匹配 vs 模糊匹配
		if entName, ok := candidate["ent_name"].(string); ok {
			if entName == keyword {
				candidate["match_hint"] = "exact"
			} else if len(entName) > 0 && (strings.Contains(entName, keyword) || strings.Contains(keyword, entName)) {
				candidate["match_hint"] = "contains"
			} else {
				candidate["match_hint"] = "fuzzy"
			}
		}

		if len(candidate) > 0 {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}
