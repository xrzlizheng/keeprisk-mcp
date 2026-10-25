package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 校企域 pcode（目录元数据已在 internal/catalog/overrides.go 修正）
const (
	pcodeSchoolInfo   = "E_006_0011" // 院校信息：按 universityName 解析院校（支持简称，如 中科大→中国科学技术大学）
	pcodeSchoolFilter = "E_006_0021" // 校企筛选V2：schoolName 按校名筛校友圈企业；companyId 反查该企业人员的毕业院校
	pcodeSchoolStats  = "E_017_0091" // 校企统计：校友企业地域/行业分布
	pcodeBasicProfile = "E_007_0501" // 工商基本信息：返回内部 id（companyId 类查询的钥匙）与 legalName
)

// SchoolAlumniOpts 校友圈企业查询参数（双入口 + 区划 + 分页）
type SchoolAlumniOpts struct {
	UniversityName string // 院校官方全称（简称会被自动解析）
	CompanyName    string // 企业名称（由其法人院校反查）
	Province       string // 区划：省（city/county 必须与之同传）
	City           string
	County         string
	PageSize       int
	Page           int // 起始页码（映射上游 pageNo）
	MaxPages       int // 连续扫描页数
	IncludeStats   bool
}

// buildRegionList 构造上游要求的 regionList（数组嵌套：[{province,city,county}]）。
//
// 实测依据（2026-09-17，逐项线上验证）：
//   - regionList=[{province}] / [{province,city}] / [{province,city,county}] 均生效，逐级收窄
//     （安徽 → 全是安徽城市；加合肥市 → 全合肥；再加蜀山区 → 20/20 蜀山区）；
//   - 扁平 province/county（目录原声明）被上游**静默忽略且照常计费**——返回与无过滤完全相同的结果；
//   - 仅传 city/county 而不传 province 同样被忽略。
//
// 因此：province 为区划的必需锚点，缺省时本地直接报错（静默无效比报错更危险）。
func buildRegionList(province, city, county string) ([]map[string]string, string) {
	province, city, county = strings.TrimSpace(province), strings.TrimSpace(city), strings.TrimSpace(county)
	if province == "" && city == "" && county == "" {
		return nil, ""
	}
	if province == "" {
		return nil, missingEntryArg(entryArgSpec{
			Tool: "school_alumni_circle", Canonical: "province",
			Meaning: "省份（区划筛选的必需锚点；实测仅传 city/county 会被上游静默忽略且计费）",
			Example: map[string]any{"province": "安徽", "city": "合肥市"},
			Hint:    "若不需要区划筛选，请不要传 province/city/county（默认全国）。",
		})
	}
	ri := map[string]string{"province": province}
	if city != "" {
		ri["city"] = city
	}
	if county != "" {
		ri["county"] = county
	}
	return []map[string]string{ri}, ""
}

// SchoolAlumniCircle 校友圈企业：两种入口。
//
//	① university_name：按院校查该校校友创办/任职的企业（原路径）；
//	② company_name：按企业查其法人（或核心人员）的毕业院校，再给出该院校的校友圈企业。
//
// 设计动机（2026-09-16 通用模型盲测）：
// 原始链路要求模型自行发现「校企筛选V2 + type/text」这一未文档化用法
// （11 个参数中 text 无提示、自然用法 searchKey 直接返回 500），且上游 text
// 过滤不严格（返回中混有非该校校友企业）。本工具把该链路收口为一次调用，
// 并对校友学历做二次校验，只保留真实命中。
//
// 企业入口的必要性（2026-09-17 二轮盲测发现，已实测验证）：
// 「校企筛选V2」的 companyId 参数可返回该企业人员的毕业院校——三个独立盲测代理
// 均指出首版 `_note`（"无法由企业名反查院校"）是最大绕路源：该断言对「院校信息」
// 的 entName 成立，但忽略了 companyId 维度。实测：智侒信 companyId →
// 法人陈康康 = 哈尔滨工业大学（pId 与「主要人员」staffId 一致，确认为同一人）。
// 故新增 company_name 入口，把"企业→法人→院校→校友圈"整链收口。
//
// 简称处理（2026-09-16 复审，实测依据）：
//   - 上游「院校信息」支持简称解析（传"中科大"返回"中国科学技术大学"），
//     因此本工具不自建别名表（长尾别名不可维护），而是对"缺院校全称特征"的输入
//     先解析为官方全称再筛选。
//   - 必要性：简称若直接进筛选，会得到 scanned>0 但 matched=0 的响应，被读成
//     "该校没有校友企业"——静默假阴性比报错更危险。解析失败时明确报错，不返回空结果。
func (s *Services) SchoolAlumniCircle(ctx context.Context, opts SchoolAlumniOpts) string {
	universityName, companyName := opts.UniversityName, opts.CompanyName
	pageSize, startPage, maxPages, includeStats := opts.PageSize, opts.Page, opts.MaxPages, opts.IncludeStats
	// 区划：扁平入参 → upstream regionList（平铺会被静默忽略且计费，见 buildRegionList）
	regionList, regionErr := buildRegionList(opts.Province, opts.City, opts.County)
	if regionErr != "" {
		return regionErr
	}
	schoolInput := strings.TrimSpace(universityName)
	companyInput := strings.TrimSpace(companyName)
	if schoolInput == "" && companyInput == "" {
		return mustJSON(map[string]any{
			"error":   "validation",
			"message": "请提供 university_name（院校官方全称，如 中国科学技术大学）或 company_name（企业名称，由其法人院校反查），二选一。",
		})
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 100 {
		pageSize = 100
	}
	if maxPages <= 0 {
		maxPages = 1
	}
	if maxPages > 10 { // 成本护栏：每页 1 次网关调用
		maxPages = 10
	}
	startPage = pageStart(startPage)

	// ── 企业入口：企业 → 法人/核心人员 → 院校 ──
	viaCompany := ""
	if schoolInput == "" {
		company, legalPerson, personnel := s.lookupCompanyPersonnel(ctx, companyInput)
		if company == nil {
			return mustJSON(map[string]any{
				"error": "validation",
				"message": fmt.Sprintf("未找到企业「%s」或该企业无人员学历数据。请用 resolve_entity 或 fuzzy_search 核对企业全称后重试。", companyInput),
			})
		}
		uni := ""
		if legalPerson != "" {
			for _, p := range personnel {
				if p["name"] == legalPerson {
					uni, _ = p["university"].(string)
					break
				}
			}
		}
		if uni == "" {
			// 法人无学历数据时不静默返回空：给出全部人员学历，交由调用方判断
			return mustJSON(map[string]any{
				"company":      company["ent_name"],
				"legal_person": legalPerson,
				"personnel":    personnel,
				"error":        "no_university_for_legal_person",
				"message": fmt.Sprintf("企业「%v」的法人 %q 在上游无学历数据；以下为该公司有学历记录的人员，可用其院校继续查询（university_name 入口）。",
					company["ent_name"], legalPerson),
				"_note": "上游 companyId 维度返回的是企业人员的学历明细；如法人缺学历，可改用任一人员的院校。",
			})
		}
		schoolInput = uni
		viaCompany = fmt.Sprintf("%v（%v）→ %s", company["ent_name"], legalPerson, uni)
	}

	school := schoolInput
	resolutionNote := ""
	if !looksLikeFullName(school) {
		full, cands := s.resolveSchoolName(ctx, school)
		if full == "" {
			return mustJSON(map[string]any{
				"error": "validation",
				"message": fmt.Sprintf("「%s」不是院校全称，且未能解析为官方全称。请改用全称重试"+
					"（如 中科大 → 中国科学技术大学）；也可先用 keeprisk_query 按接口名称「院校信息」核对。", school),
			})
		}
		if full != school {
			resolutionNote = fmt.Sprintf("已将「%s」解析为官方全称「%s」", school, full)
			if cands > 1 {
				resolutionNote += fmt.Sprintf("（上游返回 %d 个候选，已取第一个）", cands)
			}
			school = full
		}
	}

	scanned, pages, more, upstreamTotal, errOut := s.scanSchoolEnterprises(ctx, school, regionList, pageSize, startPage, maxPages)
	if errOut != "" {
		return errOut
	}
	matches, matchedAlumni := matchAlumniEnterprises(scanned, school)

	out := map[string]any{
		"school":              school,
		"pages_scanned":       pages,
		"scanned":             len(scanned),
		"matched_enterprises": len(matches),
		"matched_alumni":      matchedAlumni,
		"enterprises":         matches,
		"_note": "结果取自上游筛选结果中「确含该校学历人员」的校友企业（上游 text 过滤不严格，" +
			"本工具已做校友学历二次校验）。命中偏少时可增大 max_pages 翻页扫描（每页 1 次网关调用）。" +
			"如需由企业反查其法人院校，请传 company_name（本工具内部走「工商基本信息」→" +
			"「校企筛选V2」companyId 维度）。",
	}
	if viaCompany != "" {
		out["via_company"] = viaCompany
	}
	if resolutionNote != "" {
		out["school_input"] = schoolInput
		out["_resolution"] = resolutionNote
	}
	// 抽样透明度：明确"扫了几页/几条、总量多少、是否还有更多"，避免结果被误读为全量
	if upstreamTotal > 0 && upstreamTotal > len(scanned) {
		out["upstream_total"] = upstreamTotal
	}
	firstPage := pageStart(startPage)
	lastPage := firstPage + pages - 1
	if lastPage < firstPage {
		lastPage = firstPage
	}
	out["_sampling"] = samplingNote(firstPage, lastPage, len(scanned), more, pageSize)
	if len(matches) == 0 {
		out["_hint"] = zeroMatchHint(school, len(scanned))
	}
	if includeStats {
		out["stats"] = s.schoolAlumniStats(ctx, school)
	}
	return mustJSON(out)
}

// scanSchoolEnterprises 按页扫描校企筛选V2（pageNo 分页）。
//
// 分页依据（2026-09-17 实测）：上游「校企筛选V2」支持 pageNo（目录参数表未列出，
// 属未文档化但生效的参数）；page 是无效名会被静默忽略——曾据此误判"不支持翻页"。
//
// 返回：累计条目、实际扫描页数、是否还有更多页、上游总量、错误输出（非空表示失败）。
// 成本：每页 1 次网关调用；某页返回不足一页时提前结束。
func (s *Services) scanSchoolEnterprises(ctx context.Context, school string, regionList []map[string]string, pageSize, startPage, maxPages int) ([]any, int, bool, int, string) {
	if startPage < 1 {
		startPage = 1
	}
	if maxPages < 1 {
		maxPages = 1
	}
	var all []any
	seen := map[string]struct{}{}
	pages := 0
	more := false
	total := 0
	for i := 0; i < maxPages; i++ {
		pageNo := startPage + i
		// 按学校筛校友圈：用文档权威参数 schoolName（模糊搜索，空格为且、逗号为或）。
		// 2026-09-17 校正：此前用平铺 type=1+text —— type=1 实为"企业名称模糊搜索"，
		// 传校名会得到全国泛匹配（total 71990 vs schoolName 的 2126），语义不符。
		params := map[string]any{
			"schoolName": school,
			"pageSize":   fmt.Sprint(pageSize),
			"pageNo":     fmt.Sprint(pageNo),
		}
		if len(regionList) > 0 {
			params["regionList"] = regionList
		}
		raw := s.callQuery(ctx, pcodeSchoolFilter, params)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return all, pages, more, total, mustJSON(map[string]any{"error": "parse_error", "message": "上游响应解析失败"})
		}
		if errStr, _ := parsed["error"].(string); errStr != "" {
			// 首页失败直接报错；后续页失败则保留已得结果并说明（部分成功优于全丢）
			if pages == 0 {
				out := map[string]any{
					"error":   errStr,
					"message": parsed["message"],
					"_hint": "上游校企筛选当前返回错误；可稍后重试，或用 keeprisk_query 按接口名称" +
						"「校企筛选V2」直调复现（参数 {type:\"1\", text:\"<校名>\", pageNo:1}）",
				}
				if c, ok := parsed["code"]; ok {
					out["code"] = c
				}
				return all, pages, more, total, mustJSON(out)
			}
			more = true
			break
		}
		data, _ := parsed["data"].(map[string]any)
		list, _ := data["list"].([]any)
		if total == 0 {
			if t, ok := anyToInt(data["total"]); ok {
				total = t
			}
		}
		pages++
		for _, it := range list {
			key := ""
			if m, ok := it.(map[string]any); ok {
				if eid, _ := m["eid"].(string); eid != "" {
					key = eid
				} else if name, _ := m["entName"].(string); name != "" {
					key = "n:" + name
				}
			}
			if key != "" {
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
			}
			all = append(all, it)
		}
		if len(list) < pageSize {
			break // 已到末页
		}
		if i == maxPages-1 {
			more = true // 末页满且有扫描上限 → 可能还有更多
		}
	}
	return all, pages, more, total, ""
}

// samplingNote 生成抽样说明（几页/几条/是否还有更多）
func samplingNote(firstPage, lastPage, scanned int, more bool, pageSize int) string {
	scope := fmt.Sprintf("已扫描第 %d 页", firstPage)
	if lastPage > firstPage {
		scope = fmt.Sprintf("已扫描第 %d–%d 页", firstPage, lastPage)
	}
	tail := "上游还有更多页，可增大 max_pages 继续扫描（每页 1 次网关调用）。"
	if !more {
		tail = "已扫描至末页。"
	}
	return fmt.Sprintf("%s、共 %d 条（每页最多 %d 条）；结果为企业列表的抽样而非全量——%s",
		scope, scanned, pageSize, tail)
}

// pageStart 归一化起始页（默认 1）
func pageStart(p int) int {
	if p < 1 {
		return 1
	}
	return p
}

// lookupCompanyPersonnel 企业入口：企业名 → 内部 id → 该企业人员学历明细。
// 返回企业摘要（含 ent_name）、法人姓名、人员学历列表；企业不存在时返回 nil。
func (s *Services) lookupCompanyPersonnel(ctx context.Context, companyName string) (company map[string]any, legalPerson string, personnel []map[string]any) {
	raw := s.callQuery(ctx, pcodeBasicProfile, map[string]any{"entName": companyName})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, "", nil
	}
	if errStr, _ := parsed["error"].(string); errStr != "" {
		return nil, "", nil
	}
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		return nil, "", nil
	}
	id, _ := data["id"].(string)
	entName, _ := data["entName"].(string)
	legalPerson, _ = data["legalName"].(string)
	if strings.TrimSpace(entName) == "" {
		return nil, "", nil
	}
	company = map[string]any{"ent_name": entName, "legal_person": legalPerson}
	if strings.TrimSpace(id) == "" {
		return company, legalPerson, nil // 无内部 id 则无法查人员学历
	}

	// companyId 维度：返回该企业人员的毕业院校明细
	raw2 := s.callQuery(ctx, pcodeSchoolFilter, map[string]any{"companyId": id, "pageSize": "100"})
	var p2 map[string]any
	if err := json.Unmarshal([]byte(raw2), &p2); err != nil {
		return company, legalPerson, nil
	}
	if errStr, _ := p2["error"].(string); errStr != "" {
		return company, legalPerson, nil
	}
	d2, _ := p2["data"].(map[string]any)
	list2, _ := d2["list"].([]any)
	for _, it := range list2 {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		alumni, _ := m["alumni"].([]any)
		for _, a := range alumni {
			am, _ := a.(map[string]any)
			if am == nil {
				continue
			}
			name, _ := am["alumniName"].(string)
			pos, _ := am["position"].(string)
			uni := firstStringInArray(am["universityName"])
			if name == "" {
				continue
			}
			personnel = append(personnel, map[string]any{
				"name": name, "position": pos, "university": uni, "p_id": am["pId"],
			})
		}
	}
	return company, legalPerson, personnel
}

// firstStringInArray 取数组首元素字符串（universityName 为数组形态）
func firstStringInArray(v any) string {
	arr, _ := v.([]any)
	for _, it := range arr {
		if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// resolveSchoolName 用「院校信息」把简称/别名解析为官方全称（1 次网关调用，可缓存）。
// 返回首个命中的院校全称与上游候选数；解析失败返回空串。
func (s *Services) resolveSchoolName(ctx context.Context, input string) (full string, candidates int) {
	raw := s.callQuery(ctx, pcodeSchoolInfo, map[string]any{"universityName": input})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", 0
	}
	if errStr, _ := parsed["error"].(string); errStr != "" {
		return "", 0
	}
	data, _ := parsed["data"].(map[string]any)
	list, _ := data["list"].([]any)
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if name, _ := m["name"].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name), len(list)
		}
	}
	return "", 0
}

// looksLikeFullName 判断名称是否具备院校全称特征。
//
// 仅用于决定"是否需要先解析简称"，不参与匹配。依据：中文院校全称必含
// 大学/学院/学校/职业/高等专科 之一（如 中国科学技术大学、清华大学、
// 北京电子科技职业学院），而常见简称（中科大/清华/北大/浙大）均不含。
func looksLikeFullName(s string) bool {
	for _, marker := range []string{"大学", "学院", "学校", "职业", "专科", "研究所"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// zeroMatchHint 生成 0 命中时的行动指引。
//
// 关键：不能让 0 命中被读成"该校没有校友企业"——必须给出可执行的下一步。
func zeroMatchHint(school string, scanned int) string {
	if scanned == 0 {
		return fmt.Sprintf("上游未返回任何候选企业（text=%q）。可能是校名与上游索引不一致："+
			"请用 keeprisk_query 按接口名称「院校信息」核对官方全称后重试。", school)
	}
	return fmt.Sprintf("已扫描 %d 家企业，但未发现确含「%s」学历的人员：该校可能暂无校友企业数据，"+
		"或本页窗口覆盖不全。可尝试：① 用 max_pages 增大扫描页数（每页 1 次网关调用）；"+
		"② 用「院校信息」核对校名；③ 用「校企筛选V2」直调核对上游原始返回。", scanned, school)
}

// matchAlumniEnterprises 从上游响应中筛出「确含该校学历校友」的企业，按命中校友数降序。
//
// 匹配归一化：去空格 + 忽略大小写 + 双向包含（兼容"清华大学"/"清华大学 "及
// 官方名与展示名的细微差异）。不做简称推断（如"中科大"→"中国科学技术大学"），
// 简称请调用方先补全——避免模糊匹配把非目标院校企业混入。
func matchAlumniEnterprises(list []any, school string) ([]map[string]any, int) {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	}
	want := norm(school)
	if want == "" {
		return nil, 0
	}
	var out []map[string]any
	totalAlumni := 0
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		var hits []map[string]any
		alumni, _ := m["alumni"].([]any)
		for _, a := range alumni {
			am, _ := a.(map[string]any)
			if am == nil {
				continue
			}
			unis, _ := am["universityName"].([]any)
			for _, u := range unis {
				us, _ := u.(string)
				if us == "" {
					continue
				}
				nu := norm(us)
				if strings.Contains(nu, want) || strings.Contains(want, nu) {
					hits = append(hits, map[string]any{
						"name":       am["alumniName"],
						"position":   am["position"],
						"university": us,
					})
					break
				}
			}
		}
		if len(hits) == 0 {
			continue
		}
		totalAlumni += len(hits)
		out = append(out, map[string]any{
			"ent_name":       m["entName"],
			"city":           m["city"],
			"county":         m["county"],
			"ent_status":     m["entStatus"],
			"matched_alumni": hits,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := out[i]["matched_alumni"].([]map[string]any)
		b, _ := out[j]["matched_alumni"].([]map[string]any)
		return len(a) > len(b)
	})
	if len(out) > 30 { // 控制单次响应体积
		out = out[:30]
	}
	return out, totalAlumni
}

// schoolAlumniStats 校友企业地域/行业分布摘要（+1 次网关调用；失败不阻断主结果）
func (s *Services) schoolAlumniStats(ctx context.Context, school string) map[string]any {
	raw := s.callQuery(ctx, pcodeSchoolStats, map[string]any{"schoolName": school})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]any{"error": "parse_error"}
	}
	if errStr, _ := parsed["error"].(string); errStr != "" {
		return map[string]any{"error": errStr, "message": parsed["message"]}
	}
	data, _ := parsed["data"].(map[string]any)
	out := map[string]any{}
	if v, ok := data["entNum"]; ok {
		out["total_enterprises"] = v
	}
	out["top_regions"] = topNByName(data["area"], 5)
	out["top_industries"] = topNByName(data["industry"], 5)
	return out
}

// topNByName 取 [{name,entNum}] 形态数组的 TopN（按 entNum 降序）。
func topNByName(v any, n int) []map[string]any {
	arr, _ := v.([]any)
	type row struct {
		Name string
		Num  float64
	}
	rows := make([]row, 0, len(arr))
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		num, _ := m["entNum"].(float64)
		rows = append(rows, row{name, num})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Num > rows[j].Num })
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = map[string]any{"name": r.Name, "ent_num": r.Num}
	}
	return out
}
