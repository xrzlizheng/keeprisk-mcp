package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 本文件从 server.go 拆分而来，集中存放企业搜索/产业链搜索的请求参数构造逻辑。
// 共享辅助函数 intArg / ensureChainSuffix 仍保留在 server.go 中（被其他工具实现引用）。

// ─────────────────────────── 产业链搜索参数 ───────────────────────────

func buildChainSearchParams(args map[string]any) map[string]any {
	chain, _ := args["chain"].(string)
	province, _ := args["province"].(string)
	city, _ := args["city"].(string)
	county, _ := args["county"].(string)
	node, _ := args["node_code"].(string)
	status, _ := args["company_status"].(string)
	keyword, _ := args["keyword"].(string)
	creditCode, _ := args["credit_code"].(string)
	economicRegion, _ := args["economic_region"].(string)
	industry, _ := args["industry"].(string)
	sortOrder, _ := args["sort_order"].(string)
	page := intArg(args, "page", 1)
	ps := intArg(args, "page_size", 20)
	if ps > 50 {
		ps = 50
	}
	if ps < 1 {
		ps = 20
	}
	params := map[string]any{
		"chainHis": ensureChainSuffix(chain),
		"pageNo":   page,
		"pageSize": ps,
	}
	if strings.TrimSpace(node) != "" {
		params["nodeCode"] = strings.TrimSpace(node)
	}

	// 地区：regionList [{province,city,county}]
	if strings.TrimSpace(province) != "" || strings.TrimSpace(city) != "" {
		ri := map[string]string{}
		if strings.TrimSpace(province) != "" {
			ri["province"] = strings.TrimSpace(province)
		}
		if strings.TrimSpace(city) != "" {
			ri["city"] = strings.TrimSpace(city)
		}
		if strings.TrimSpace(county) != "" {
			ri["county"] = strings.TrimSpace(county)
		}
		params["regionList"] = []map[string]string{ri}
	}
	if strings.TrimSpace(economicRegion) != "" {
		params["economicRegion"] = strings.TrimSpace(economicRegion)
	}

	// 排除地区：excludeRegionList [{province,city,county}]（PDF 有但原代码遗漏）
	// 支持传入 exclude_provinces/exclude_cities/exclude_counties 数组，或直接 exclude_region_list 对象数组
	if arr := regionListArg(args, "exclude_region_list"); len(arr) > 0 {
		params["excludeRegionList"] = arr
	} else {
		ep := stringArrayArg(args, "exclude_provinces")
		ec := stringArrayArg(args, "exclude_cities")
		eco := stringArrayArg(args, "exclude_counties")
		if len(ep) > 0 || len(ec) > 0 || len(eco) > 0 {
			exList := make([]map[string]string, 0)
			for _, p := range ep {
				exList = append(exList, map[string]string{"province": p})
			}
			for _, c := range ec {
				exList = append(exList, map[string]string{"city": c})
			}
			for _, co := range eco {
				exList = append(exList, map[string]string{"county": co})
			}
			params["excludeRegionList"] = exList
		}
	}

	// 模糊搜索字段（支持 eq/no_eq/type 组合）
	if strings.TrimSpace(keyword) != "" {
		params["entName"] = strings.TrimSpace(keyword)
	}
	if strings.TrimSpace(creditCode) != "" {
		params["creditCode"] = strings.TrimSpace(creditCode)
	}

	// businessScope/regAddr/legalPersonNames 支持 eq(包含)/noEq(不包含)/type(1并且/2或者)
	for _, field := range []struct{ k, key string }{
		{"business_scope", "businessScope"},
		{"reg_addr", "regAddr"},
		{"legal_person_names", "legalPersonNames"},
	} {
		base, _ := args[field.k].(string)
		eq := stringArrayArg(args, field.k+"_eq")
		noEq := stringArrayArg(args, field.k+"_no_eq")
		t := intArg(args, field.k+"_type", 0)
		if base != "" || len(eq) > 0 || len(noEq) > 0 || t > 0 {
			obj := map[string]any{}
			if base != "" {
				obj["eq"] = base
			}
			if len(eq) > 0 {
				obj["eq"] = strings.Join(eq, " ")
			}
			if len(noEq) > 0 {
				obj["noEq"] = strings.Join(noEq, " ")
			}
			if t == 1 || t == 2 {
				obj["type"] = t
			}
			params[field.key] = obj
		}
	}

	// companyOrgType 支持字符串数组（["集体所有制","个体工商户"]）或整数码数组
	if arr := stringArrayArg(args, "company_org_type"); len(arr) > 0 {
		params["companyOrgType"] = arr
	}

	// 行业（支持一级到四级）
	if arr := stringArrayArg(args, "industry_list"); len(arr) > 0 {
		list := make([]map[string]string, 0, len(arr))
		for _, s := range arr {
			list = append(list, map[string]string{"oneLevel": s})
		}
		params["industryList"] = list
	} else if strings.TrimSpace(industry) != "" {
		params["industryList"] = []map[string]string{{"oneLevel": strings.TrimSpace(industry)}}
	}
	// 四级行业对象参数 [{oneLevel,twoLevel,treeLevel,fourLevel}]
	if arr := industryLevelsArg(args, "industry_levels"); len(arr) > 0 {
		params["industryList"] = arr
	}

	// 状态（支持整数码或字符串码）
	if st := chainStatusMap(strings.TrimSpace(status)); st != "" {
		params["companyState"] = []string{st}
	}
	// 直接传整数码数组
	if arr := intArrayArg(args, "company_status"); len(arr) > 0 {
		strs := make([]string, 0, len(arr))
		for _, n := range arr {
			strs = append(strs, fmt.Sprint(n))
		}
		params["companyState"] = strs
	}

	// eq/no_eq 与 binding_type/search_logic_type（顶级）
	if v := stringArrayArg(args, "eq"); len(v) > 0 {
		params["eq"] = strings.Join(v, " ")
	}
	if v := stringArrayArg(args, "no_eq"); len(v) > 0 {
		params["noEq"] = strings.Join(v, " ")
	}
	if n := intArg(args, "binding_type", 0); n == 1 || n == 2 {
		params["bindingType"] = n
	}
	if n := intArg(args, "search_logic_type", 0); n == 1 || n == 2 {
		params["type"] = n
	}

	// 多选码表参数
	for _, kv := range []struct{ k, out string }{
		{"company_org_type", "companyOrgType"},
		{"total_sales_range", "totalSalesRange"},
		{"total_taxation_range", "totalTaxationRange"},
		{"listed", "listed"},
		{"technology_state", "technologyState"},
		{"exist_type", "existType"},
		{"no_exist_type", "noExistType"},
		{"finance", "finance"},
	} {
		if arr := intArrayArg(args, kv.k); len(arr) > 0 {
			params[kv.out] = arr
		}
	}
	// capitalType 特殊处理：支持整数码数组 [1,2] 或字符串数组 ["全部","美元"]
	if arr := intArrayArg(args, "capital_type"); len(arr) > 0 {
		params["capitalType"] = arr
	} else if arr := stringArrayArg(args, "capital_type"); len(arr) > 0 {
		params["capitalType"] = arr
	}

	// 区间对象数组
	for _, kv := range []struct{ k, out string }{
		{"reg_capital", "regCapital"},
		{"contributors_ins", "contributorsIns"},
		{"establish_times", "establishTimes"},
		{"chain_rank_list", "chainRankList"},
	} {
		if arr := rangeArrayArg(args, kv.k); len(arr) > 0 {
			params[kv.out] = arr
		}
	}

	// 排序
	if s := strings.ToLower(strings.TrimSpace(sortOrder)); s == "asc" || s == "desc" {
		params["sortOrder"] = s
	}
	if n := intArg(args, "field_sort", 0); n > 0 {
		params["sort"] = map[string]any{"fieldSort": n}
	}
	if v, ok := args["sort_type"].(bool); ok {
		params["sortType"] = v
	}

	// 企业坐标+距离筛选（PDF point 对象：type/km/lon/lat/local/sortOrder）
	pointObj := buildPointParam(args)
	if pointObj != nil {
		params["point"] = pointObj
	}

	// 投资意愿评分地区
	if single := rangeMapArg(args, "score_region_map"); single != nil {
		params["scoreRegionMap"] = single
	}

	return params
}

// buildPointParam 构建 PDF 中的 point 对象（企业坐标+距离筛选）
// 支持扁平参数：point_type/point_km/point_lon/point_lat/point_local/point_sort_order
// 也支持直接传入嵌套 point 对象。point_local 在 type=2 时为多边形坐标点数组。
func buildPointParam(args map[string]any) map[string]any {
	if raw, ok := args["point"].(map[string]any); ok && len(raw) > 0 {
		return normalizePointObj(raw)
	}

	pt := intArg(args, "point_type", 0)
	km := args["point_km"]
	lon := args["point_lon"]
	lat := args["point_lat"]
	local := parsePointLocal(args["point_local"])
	sortOrd, _ := args["point_sort_order"].(string)

	if pt == 0 && km == nil && lon == nil && lat == nil && local == nil {
		return nil
	}
	obj := map[string]any{}
	if pt > 0 {
		obj["type"] = pt
	}
	if km != nil {
		obj["km"] = km
	}
	if lon != nil {
		obj["lon"] = lon
	}
	if lat != nil {
		obj["lat"] = lat
	}
	if local != nil {
		obj["local"] = local
	}
	if s := strings.ToLower(strings.TrimSpace(sortOrd)); s == "asc" || s == "desc" {
		obj["sortOrder"] = s
	}
	return obj
}

func normalizePointObj(raw map[string]any) map[string]any {
	obj := map[string]any{}
	if v, ok := raw["type"]; ok {
		obj["type"] = v
	}
	if v, ok := raw["km"]; ok {
		obj["km"] = v
	}
	if v, ok := raw["lon"]; ok {
		obj["lon"] = v
	}
	if v, ok := raw["lat"]; ok {
		obj["lat"] = v
	}
	if local := parsePointLocal(raw["local"]); local != nil {
		obj["local"] = local
	}
	if s, ok := raw["sortOrder"].(string); ok {
		if order := strings.ToLower(strings.TrimSpace(s)); order == "asc" || order == "desc" {
			obj["sortOrder"] = order
		}
	}
	if len(obj) == 0 {
		return nil
	}
	return obj
}

// parsePointLocal 解析多边形坐标点集合（[]{lon,lat}），兼容旧的字符串地名。
func parsePointLocal(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return s
		}
	case []map[string]string:
		if len(t) > 0 {
			return t
		}
	case []any:
		out := make([]map[string]string, 0, len(t))
		for _, x := range t {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			item := map[string]string{}
			if lon := fmt.Sprint(m["lon"]); lon != "" && lon != "<nil>" {
				item["lon"] = lon
			}
			if lat := fmt.Sprint(m["lat"]); lat != "" && lat != "<nil>" {
				item["lat"] = lat
			}
			if item["lon"] != "" && item["lat"] != "" {
				out = append(out, item)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// regionListArg 从 args 中提取 [{province,city,county}] 地区对象数组
// 支持 []any（每个元素为 map[string]any）格式
func regionListArg(args map[string]any, key string) []map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		item := map[string]string{}
		for _, k := range []string{"province", "city", "county", "economicRegion"} {
			if val := fmt.Sprint(m[k]); val != "" && val != "<nil>" {
				item[k] = val
			}
		}
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func chainStatusMap(s string) string {
	m := map[string]string{
		"在业": "1", "开业": "1", "在营": "1", "经营": "1",
		"存续": "2", "筹建": "3", "清算": "4", "迁入": "5", "迁出": "6", "停业": "7",
		"撤销": "8", "吊销": "9", "注销": "10", "歇业": "11", "责令关闭": "12", "除名": "13",
	}
	return m[s]
}

// ─────────────────────────── 企业搜索参数 ───────────────────────────

func buildEnterpriseSearchParams(args map[string]any) map[string]any {
	keyword, _ := args["keyword"].(string)
	province, _ := args["province"].(string)
	city, _ := args["city"].(string)
	county, _ := args["county"].(string)
	searchIn, _ := args["search_in"].(string)
	industry, _ := args["industry"].(string)
	status, _ := args["company_status"].(string)
	sortOrder, _ := args["sort_order"].(string)
	page := intArg(args, "page", 1)
	ps := intArg(args, "page_size", 20)
	if ps > 50 {
		ps = 50
	}
	p := map[string]any{"pageNo": page, "pageSize": ps}

	// 公司 id 数组直查（company_id_list），用于已知 id 批量筛选
	var cids []string
	if v, ok := args["company_ids"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				cids = append(cids, strings.TrimSpace(s))
			}
		}
	}
	if len(cids) > 0 {
		p["companyIdList"] = cids
	}

	// 关键词（支持多类型，缺省按经营范围搜索）
	if strings.TrimSpace(keyword) != "" {
		st := enterpriseSearchType(strings.TrimSpace(searchIn))
		p["searchKey"] = map[string]any{"type": st, "text": strings.TrimSpace(keyword)}
	}

	// 地区：[{pr, ci, co}]
	if strings.TrimSpace(province) != "" || strings.TrimSpace(city) != "" {
		ri := map[string]string{}
		if strings.TrimSpace(province) != "" {
			ri["pr"] = strings.TrimSpace(province)
		}
		if strings.TrimSpace(city) != "" {
			ri["ci"] = strings.TrimSpace(city)
		}
		if strings.TrimSpace(county) != "" {
			ri["co"] = strings.TrimSpace(county)
		}
		p["region"] = []map[string]string{ri}
	}

	// 行业
	if strings.TrimSpace(industry) != "" {
		p["industry"] = []map[string]string{{"oneLevel": strings.TrimSpace(industry)}}
	}

	// 状态
	if n := enterpriseStatusInt(strings.TrimSpace(status)); n != 0 {
		p["companyState"] = []int{n}
	}

	// 排序
	if s := strings.ToLower(strings.TrimSpace(sortOrder)); s == "asc" || s == "desc" {
		p["sortOrder"] = s
	}
	if n := intArg(args, "field_sort", 0); n > 0 {
		p["fieldSort"] = n
	}
	if v, ok := args["sort_type"].(bool); ok {
		p["sortType"] = v
	}

	// 财务/资产范围筛选（int 数组，按文档码表）
	for _, kv := range []struct{ k, out string }{
		{"assets_range", "assetsRange"},
		{"sales_range", "salesRange"},
		{"retained_profit_range", "retainedProfitRange"},
		{"sales_rate", "salesRate"},
		{"retained_profit_rate", "retainedProfitRate"},
		{"high_quality_ent", "highQualityEnt"},
		{"capital_type", "capitalType"},
		{"finance", "finance"},
		{"listed", "listed"},
		{"security_market", "securityMarket"},
		{"security_type", "securityType"},
		{"listed_list", "listedList"},
		{"technology", "technology"},
		{"exist_type", "existType"},
		{"no_exist_type", "noExistType"},
		{"org_type", "orgType"},
	} {
		if arr := intArrayArg(args, kv.k); len(arr) > 0 {
			p[kv.out] = arr
		}
	}

	// 字符串数组参数
	for _, kv := range []struct{ k, out string }{
		{"company_type", "companyType"},
		{"ent_org_type", "entOrgType"},
		{"pat_types", "patTypes"},
		{"honor_type", "honorType"},
		{"ent_scale_list", "entScaleList"},
	} {
		if arr := stringArrayArg(args, kv.k); len(arr) > 0 {
			p[kv.out] = arr
		}
	}

	// 区间对象数组参数 [{from,to}]
	for _, kv := range []struct{ k, out string }{
		{"reg_capital_garden", "regCapitalGarden"},
		{"establish_times", "establishTimes"},
		{"contributors_ins", "contributorsIns"},
		{"staff_num_list_map", "staffNumListMap"},
		{"comprehensive_score_list_map", "comprehensiveScoreListMap"},
		{"innovation_score_list_map", "innovationScoreListMap"},
		{"real_cap_list_map", "realCapListMap"},
		{"external_city_times", "externalCityTimes"},
	} {
		if arr := rangeArrayArg(args, kv.k); len(arr) > 0 {
			p[kv.out] = arr
		}
	}
	if single := rangeMapArg(args, "honor_time_map"); single != nil {
		p["honorTimeMap"] = single
	}

	return p
}

// ─────────────────────────── 搜索参数辅助函数 ───────────────────────────
// 以下辅助函数仅被 buildChainSearchParams / buildEnterpriseSearchParams 使用，
// 故随二者一同迁移至本文件。intArg / ensureChainSuffix 因被 server.go 中其他工具实现共享，仍保留在 server.go。

// intArrayArg 从 args 中提取整数数组（支持 []int / []any / 逗号分隔字符串）
func intArrayArg(args map[string]any, key string) []int {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []int:
		return t
	case []any:
		var out []int
		for _, x := range t {
			switch n := x.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			case json.Number:
				i, _ := n.Int64()
				out = append(out, int(i))
			case string:
				if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
					out = append(out, i)
				}
			}
		}
		return out
	case string:
		var out []int
		for _, s := range strings.Split(t, ",") {
			s = strings.TrimSpace(s)
			if i, err := strconv.Atoi(s); err == nil {
				out = append(out, i)
			}
		}
		return out
	}
	return nil
}

// stringArrayArg 从 args 中提取字符串数组（支持 []string / []any / 逗号分隔字符串）
func stringArrayArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// rangeArrayArg 从 args 中提取 [{from,to}] 区间对象数组
// 支持 []any（每个元素为 map[string]any{from,to}）或字符串简写 "from,to|from,to"
func rangeArrayArg(args map[string]any, key string) []map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		var out []map[string]string
		for _, x := range t {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			item := map[string]string{}
			if from := fmt.Sprint(m["from"]); from != "" && from != "<nil>" {
				item["from"] = from
			}
			if to := fmt.Sprint(m["to"]); to != "" && to != "<nil>" {
				item["to"] = to
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case []map[string]any:
		var out []map[string]string
		for _, m := range t {
			item := map[string]string{}
			if from := fmt.Sprint(m["from"]); from != "" && from != "<nil>" {
				item["from"] = from
			}
			if to := fmt.Sprint(m["to"]); to != "" && to != "<nil>" {
				item["to"] = to
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case string:
		// 简写："from,to|from,to" 或 "from,to;from,to"
		sep := "|"
		if strings.Contains(t, ";") {
			sep = ";"
		}
		var out []map[string]string
		for _, part := range strings.Split(t, sep) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			fields := strings.Split(part, ",")
			if len(fields) >= 2 {
				out = append(out, map[string]string{
					"from": strings.TrimSpace(fields[0]),
					"to":   strings.TrimSpace(fields[1]),
				})
			}
		}
		return out
	}
	return nil
}

// rangeMapArg 从 args 中提取单个 {from,to} 对象
func rangeMapArg(args map[string]any, key string) map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	item := map[string]string{}
	if from := fmt.Sprint(m["from"]); from != "" && from != "<nil>" {
		item["from"] = from
	}
	if to := fmt.Sprint(m["to"]); to != "" && to != "<nil>" {
		item["to"] = to
	}
	if len(item) == 0 {
		return nil
	}
	return item
}

// industryLevelsArg 解析四级行业对象参数 [{oneLevel,twoLevel,treeLevel,fourLevel}]
// 支持格式：
//   - []any/[]map[string]any: [{"oneLevel":"制造业","twoLevel":"..."}, ...]
//   - 字符串简写: "一级|二级|三级|四级" 或 "一级|二级|三级"（最多四级，最少一级）
func industryLevelsArg(args map[string]any, key string) []map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		var out []map[string]string
		for _, x := range t {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			item := map[string]string{}
			if v := fmt.Sprint(m["oneLevel"]); v != "" && v != "<nil>" {
				item["oneLevel"] = v
			}
			if v := fmt.Sprint(m["twoLevel"]); v != "" && v != "<nil>" {
				item["twoLevel"] = v
			}
			if v := fmt.Sprint(m["treeLevel"]); v != "" && v != "<nil>" {
				item["treeLevel"] = v
			}
			if v := fmt.Sprint(m["fourLevel"]); v != "" && v != "<nil>" {
				item["fourLevel"] = v
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case []map[string]any:
		var out []map[string]string
		for _, m := range t {
			item := map[string]string{}
			if v := fmt.Sprint(m["oneLevel"]); v != "" && v != "<nil>" {
				item["oneLevel"] = v
			}
			if v := fmt.Sprint(m["twoLevel"]); v != "" && v != "<nil>" {
				item["twoLevel"] = v
			}
			if v := fmt.Sprint(m["treeLevel"]); v != "" && v != "<nil>" {
				item["treeLevel"] = v
			}
			if v := fmt.Sprint(m["fourLevel"]); v != "" && v != "<nil>" {
				item["fourLevel"] = v
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case string:
		// 简写格式: "一级|二级|三级|四级"（最多四级，最少一级）
		if strings.TrimSpace(t) == "" {
			return nil
		}
		parts := strings.Split(t, "|")
		if len(parts) == 0 {
			return nil
		}
		item := map[string]string{}
		levels := []string{"oneLevel", "twoLevel", "treeLevel", "fourLevel"}
		for i, part := range parts {
			if i >= 4 {
				break
			}
			part = strings.TrimSpace(part)
			if part != "" {
				item[levels[i]] = part
			}
		}
		if len(item) > 0 {
			return []map[string]string{item}
		}
	}
	return nil
}

func enterpriseSearchType(searchIn string) string {
	// 优先支持数字码（1-11）
	switch searchIn {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11":
		return searchIn
	}
	m := map[string]string{
		"企业名称": "1", "统一社会信用代码": "2", "注册号": "3", "法人名称": "4",
		"注册地址": "5", "经营范围": "6", "联系电话": "7", "证券代码": "8", "证券简称": "9",
		"品牌产品": "10", "商标": "11",
	}
	if v, ok := m[searchIn]; ok {
		return v
	}
	return "6"
}

func enterpriseStatusInt(s string) int {
	m := map[string]int{
		"在业": 1, "开业": 1, "在营": 1, "经营": 1,
		"存续": 2, "筹建": 3, "清算": 4, "迁入": 5, "迁出": 6, "停业": 7,
		"撤销": 8, "吊销": 9, "注销": 10, "歇业": 11, "责令关闭": 12, "除名": 13,
	}
	return m[s]
}
