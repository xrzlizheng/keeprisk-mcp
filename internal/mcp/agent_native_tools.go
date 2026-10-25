package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// agentMeta 反幻觉元数据（对齐企查查 Agent-Native：可审计、可解释空数据）
func agentMeta(entity string) map[string]any {
	return map[string]any{
		"data_source":            "keeprisk-gateway",
		"queried_at":             time.Now().UTC().Format(time.RFC3339Nano),
		"entity":                 entity,
		"empty_record_code":      110,
		"empty_means":            "查询成功但无记录，不代表企业不存在",
		"hallucination_guard":    "仅使用本响应字段，禁止编造未返回的数据",
		"reference_product_note": "能力分层参考企查查智能体数据平台 agent.qcc.com 六大域",
	}
}

var (
	reCreditCode = regexp.MustCompile(`^[0-9A-HJ-NP-RTUW-Y]{2}\d{6}[0-9A-HJ-NP-RTUW-Y]{10}$`)
	rePhone      = regexp.MustCompile(`^1[3-9]\d{9}$`)
)

// FuzzySearch 统一模糊入口（对齐企查查 FuzzySearch + 统一 searchKey 理念）
func (s *Services) FuzzySearch(ctx context.Context, keyword, region string, limit int, fields string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return mustJSON(map[string]any{"error": "请提供 keyword（企业名/信用代码/电话/人名等）"})
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	full := strings.TrimSpace(strings.ToLower(fields)) == "full"

	route := "company_name"
	params := map[string]any{"pageNo": 1, "pageSize": limit}

	switch {
	case reCreditCode.MatchString(strings.ToUpper(keyword)):
		route = "credit_code"
		params["searchKey"] = map[string]any{"type": "2", "text": strings.ToUpper(keyword)}
	case rePhone.MatchString(keyword):
		route = "phone"
		params["searchKey"] = map[string]any{"type": "7", "text": keyword}
	case len([]rune(keyword)) <= 4 && !strings.Contains(keyword, "公司") && !strings.Contains(keyword, "有限"):
		// 短词且无公司特征：尝试人员模糊 + 企业名
		route = "person_or_company"
		personRaw := s.callQuery(ctx, "E_007_4301", map[string]any{"name": keyword, "pageNo": 1, "pageSize": limit})
		entRaw := s.callQuery(ctx, "E_007_1331", map[string]any{
			"pageNo": 1, "pageSize": limit,
			"searchKey": map[string]any{"type": "1", "text": keyword},
		})
		return mustJSON(map[string]any{
			"keyword":      keyword,
			"route":        route,
			"_agent_meta":  agentMeta(keyword),
			"person_hits":  jsonRaw(personRaw),
			"company_hits": slimCompanyHits(jsonRaw(entRaw), full),
			"_note":        "人员与企业双路召回（2 次计费调用）：≤4 字且不含'公司/有限'的关键词一律双路，短企业名（如'腾讯科技'）也会多调 1 次人员接口，属预期的召回完整性权衡；企业尽调请用 resolve_entity 确认主体后再查。company_hits 默认仅保留锚定字段（实测每条候选 60+ 字段、5 条即 16KB）；需要完整标签/排名字段传 fields=full。",
		})
	default:
		params["searchKey"] = map[string]any{"type": "1", "text": keyword}
	}
	if region != "" {
		params["region"] = []map[string]string{{"pr": strings.TrimSpace(region)}}
	}
	raw := s.callQuery(ctx, "E_007_1331", params)
	return mustJSON(map[string]any{
		"keyword":     keyword,
		"route":       route,
		"_agent_meta": agentMeta(keyword),
		"result":      slimCompanyHits(jsonRaw(raw), full),
		"_note":       "result 默认仅保留锚定字段（entName/creditCode/法人/状态/地区等）；需要完整标签/排名字段传 fields=full。主体锚定场景也可用 resolve_entity（候选更精简）。",
	})
}

// slimCompanyHits 企业筛选 V4（E_007_1331）候选瘦身：默认仅保留锚定字段。
// 该接口每条候选携带 60+ 字段（各级排名、坐标、数十个标签 state），
// 实测 5 条候选 16.7KB——主体锚定场景只需名称/证照/法人/状态/地区（实测瘦身后约 1/10）。
var companyAnchorFields = []string{
	"entName", "entAbbr", "creditCode", "legalPersonName", "regCapital",
	"entStatus", "regStatus", "province", "city", "county", "id", "companyId", "matchType",
}

func slimCompanyHits(v any, full bool) any {
	if full {
		return v
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if d, ok := m["data"].(map[string]any); ok {
		if lst, ok := d["list"].([]any); ok {
			out := make([]any, 0, len(lst))
			for _, it := range lst {
				if im, ok := it.(map[string]any); ok {
					nm := map[string]any{}
					for _, k := range companyAnchorFields {
						if val, has := im[k]; has {
							nm[k] = val
						}
					}
					out = append(out, nm)
				} else {
					out = append(out, it)
				}
			}
			d["list"] = out
		}
	}
	return m
}

func jsonRaw(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

// ResolveEntity 主体锚定：将模糊/简称/曾用名解析为企业候选列表（底层 E_007_1331 企业筛选V4）。
// 从 registerTools 的内联 handler 抽取为方法，便于单测通过 QueryFn 注入模拟网关响应。
func (s *Services) ResolveEntity(ctx context.Context, keyword, region string, limit int) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return mustJSON(map[string]any{"error": "请提供 keyword（企业名称关键词，必填）"})
	}
	region = strings.TrimSpace(region)
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}

	// 构造 E_007_1331 查询参数
	p := map[string]any{
		"pageNo":   1,
		"pageSize": limit,
		"searchKey": map[string]any{
			"type": "1", // 按企业名称搜索
			"text": keyword,
		},
	}
	if region != "" {
		p["region"] = []map[string]string{{"pr": region}}
	}

	raw := s.callQuery(ctx, "E_007_1331", p)

	// 解析响应，提取结构化候选列表；解析失败返回原始响应
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed == nil {
		return raw
	}

	// 检查网关错误
	if errStr, ok := parsed["error"].(string); ok && errStr != "" {
		return raw
	}

	candidates := s.buildResolveCandidates(parsed, keyword, limit)
	out := map[string]any{
		"keyword": keyword,
		"region":  region,
		// total_hits 是模糊召回上限（搜索引擎式），不是"相关企业数"——
		// 全名查询也能得到数百的值，按字面解读会严重高估匹配规模（实测全名查询 294）。
		"total_hits":       resolveTotalHits(parsed, len(candidates)),
		"candidates_shown": len(candidates),
		"candidates":       candidates,
		"_note": "请让 Agent 展示候选列表并请求用户确认后再调用尽调/穿透/实控人等工具。若 candidates_shown > 1，务必区分同名企业。total_hits 为模糊召回上限（非相关企业数），判断匹配规模请以 candidates 为准；exact 候选（如有）即为精确匹配。",
	}
	// 透传 QueryImpl 附加的网关层元数据（_cache_hit/pcode_used/warnings/_redirect）；
	// 其中含接口编码的键（pcode_used/_redirect）由统一出口 sanitizeOutput 移除。
	applyQueryMeta(out, parsed)
	return mustJSON(out)
}

// CompanyQuickScan 一句话查企业（对齐企查查「查工商+风险摘要」开箱体验）
func (s *Services) CompanyQuickScan(ctx context.Context, entName, companyID, keyword string, depth string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	keyword = strings.TrimSpace(keyword)
	if entName == "" && companyID == "" && keyword != "" {
		entName = keyword
	}
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name、company_id 或 keyword"})
	}

	depth = strings.ToLower(strings.TrimSpace(depth))
	if depth == "" {
		depth = "standard"
	}

	start := time.Now()
	entity := entName
	if entity == "" {
		entity = companyID
	}

	out := map[string]any{
		"tool":        "company_quick_scan",
		"depth":       depth,
		"_agent_meta": agentMeta(entity),
	}

	// 1. 工商照面
	basicParams := map[string]any{}
	if companyID != "" {
		basicParams["companyId"] = companyID
	}
	if entName != "" {
		basicParams["entName"] = entName
	}
	basicRaw := s.callQuery(ctx, "E_007_0501", basicParams)
	out["basic_profile"] = jsonRaw(basicRaw)

	// 2. 信用评分门卫
	creditRaw := s.EnterpriseCreditScore(ctx, entName, companyID)
	out["credit_score"] = jsonRaw(creditRaw)

	apiCalls := 7 // 1 basic + 6 credit

	// 3. 风险摘要（按深度）
	//
	// 2026-09-18 修正（飞友科技实测）：standard 档原先只查
	// shareholders,admin_penalty,abnormal_operation,enforced,dishonest —— **完全不含司法维度**，
	// 于是同一主体 quick_scan 报 overall_level=green 而全量 snapshot 报 red。
	// 现把四个"红级高频来源"（法院公告/开庭公告/裁判文书/破产重整）纳入 standard，
	// 并把实际覆盖与未覆盖维度显式写进响应，避免 green 被读成"已全面排查"。
	var ddSections string
	coverage, notCovered := []string{}, []string{}
	switch depth {
	case "light":
		ddSections = "shareholders,admin_penalty,abnormal_operation"
		coverage = []string{"工商股东", "行政处罚", "经营异常"}
		notCovered = []string{"法院公告", "开庭公告", "裁判文书", "破产重整", "失信", "被执行", "司法拍卖", "严重失信"}
	case "deep":
		ddSections = "" // 全量（受 include_judicial 控制）
		coverage = []string{"全量 26 节"}
	default: // standard
		ddSections = "shareholders,admin_penalty,abnormal_operation,enforced,dishonest," +
			"court_announcement,court_hearing,judgment,bankruptcy"
		coverage = []string{"工商股东", "行政处罚", "经营异常", "被执行", "失信",
			"法院公告", "开庭公告", "裁判文书", "破产重整"}
		notCovered = []string{"终本案件", "限制高消费", "严重失信", "司法拍卖", "司法协助", "股权出质"}
	}
	includeJudicial := depth == "deep" || depth == "standard"
	ddRaw := s.DueDiligenceSnapshot(ctx, entName, companyID, ddSections, includeJudicial, 4, true)
	out["risk_digest"] = jsonRaw(ddRaw)

	if ddParsed, ok := jsonRaw(ddRaw).(map[string]any); ok {
		if bu, ok := ddParsed["billing_units"]; ok {
			switch n := bu.(type) {
			case float64:
				apiCalls += int(n)
			case int:
				apiCalls += n
			}
		}
	}

	out["api_calls"] = apiCalls
	out["elapsed_ms"] = time.Since(start).Milliseconds()
	// 覆盖度声明：避免把 overall_level=green 读成"已全面排查、无风险"。
	// 实测教训：standard 档曾漏掉全部司法维度，同一主体本工具报 green、全量尽调报 red。
	out["risk_coverage"] = map[string]any{
		"depth":                   depth,
		"sections_checked":        coverage,
		"not_checked":             notCovered,
		"green_means":             "仅表示已检查的上述维度无异常，不等于该企业无风险",
		"escalate_when_undecided": "need_full_scan",
	}
	out["next_steps"] = []string{
		"若 risk_digest.overall_level=red → enterprise_due_diligence_snapshot(digest_only=false) 查看明细",
		"若需完整司法口径（含终本/限高/严重失信/司法拍卖）→ enterprise_due_diligence_snapshot 全量或 judicial_deep_dive",
		"若需股权结构 → control_penetration（向下投资）/ enterprise_extended_profile.beneficial_owner（向上实控人）",
		"若需上市/知产/经营 → listed_company_profile / intellectual_property_portfolio / operational_compliance",
	}
	return mustJSON(out)
}

// CompareEnterprises 多企业横向对比（2-5 家）
func (s *Services) CompareEnterprises(ctx context.Context, entNames []string, companyIDs []string) string {
	ents := trimNonEmpty(entNames)
	cids := trimNonEmpty(companyIDs)
	total := len(ents) + len(cids)
	if total < 2 {
		return mustJSON(map[string]any{"error": "请至少提供 2 家企业（ent_names 或 company_ids）"})
	}
	if total > 5 {
		return mustJSON(map[string]any{"error": "对比上限 5 家", "received": total})
	}

	type job struct {
		label string
		ent   string
		cid   string
	}
	jobs := make([]job, 0, total)
	for _, e := range ents {
		jobs = append(jobs, job{label: e, ent: e})
	}
	for _, c := range cids {
		jobs = append(jobs, job{label: c, cid: c})
	}

	results := make([]map[string]any, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(idx int, jb job) {
			defer wg.Done()
			item := map[string]any{"entity": jb.label}
			// 轻量对比：信用分 + 精简尽调 digest
			item["credit_score"] = jsonRaw(s.EnterpriseCreditScore(ctx, jb.ent, jb.cid))
			item["risk_digest"] = jsonRaw(s.DueDiligenceSnapshot(ctx, jb.ent, jb.cid,
				"admin_penalty,abnormal_operation,enforced,dishonest", true, 3, true))
			results[idx] = item
		}(i, j)
	}
	wg.Wait()

	// 排名：从 credit_score.risk_level 与 risk_digest 提取
	ranking := buildCompareRanking(results)
	return mustJSON(map[string]any{
		"tool":        "compare_enterprises",
		"count":       len(jobs),
		"companies":   results,
		"ranking":     ranking,
		"_agent_meta": agentMeta(strings.Join(ents, "|")),
		"_note":       "每家约 6+4 次 API；深度对比请对单家调 enterprise_due_diligence_snapshot。",
	})
}

func buildCompareRanking(results []map[string]any) []map[string]any {
	type row struct {
		entity string
		level  string
		score  float64
	}
	rows := make([]row, 0, len(results))
	for _, r := range results {
		ent, _ := r["entity"].(string)
		level := "unknown"
		var score float64
		if cs, ok := r["credit_score"].(map[string]any); ok {
			if rl, ok := cs["risk_level"].(string); ok {
				level = rl
			}
			if scores, ok := cs["scores"].(map[string]any); ok {
				if comp, ok := scores["composite"].(map[string]any); ok {
					if sc, ok := comp["score"].(float64); ok {
						score = sc
					}
				}
				if score == 0 {
					if comp, ok := scores["comprehensive_score"].(map[string]any); ok {
						if sc, ok := comp["score"].(float64); ok {
							score = sc
						}
					}
				}
			}
		}
		rows = append(rows, row{entity: ent, level: level, score: score})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		out = append(out, map[string]any{
			"rank":        i + 1,
			"entity":      r.entity,
			"risk_level":  r.level,
			"score":       r.score,
		})
	}
	return out
}

// PersonRiskProfile 董监高/人员风险（对齐企查查 qcc-executive 域）
func (s *Services) PersonRiskProfile(ctx context.Context, personName, entName string) string {
	personName = strings.TrimSpace(personName)
	entName = strings.TrimSpace(entName)
	if personName == "" {
		return mustJSON(map[string]any{"error": "请提供 person_name（必填）"})
	}

	start := time.Now()
	out := map[string]any{
		"tool":        "person_risk_profile",
		"person_name": personName,
		"_agent_meta": agentMeta(personName),
	}

	var wg sync.WaitGroup
	var personSearch, executives, enforced, dishonest string

	wg.Add(1)
	go func() {
		defer wg.Done()
		personSearch = s.callQuery(ctx, "E_007_4301", map[string]any{"name": personName, "pageNo": 1, "pageSize": 10})
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		enforced = s.callQuery(ctx, "E_003_0702", map[string]any{"name": personName, "pageNo": 1, "pageSize": 5})
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		dishonest = s.callQuery(ctx, "E_003_0802", map[string]any{"name": personName, "pageNo": 1, "pageSize": 5})
	}()
	if entName != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 2026-09-18 修正：此处原调 E_007_0102（企业人员董监高）。
			// 该接口声明 required_one_of=[entName,companyId]，但实测**只传 entName 必返 110**——
			// 它的真实语义是"某自然人在各企业的任职"，需要 entName+name（或 pid）才能命中。
			// 文档依据：接口文档 doc_params 注明 `pid 人员id；选填，与 entName+name 或者 companyId+name 必填其一`。
			// 实测飞友科技走 E_007_0102 时 executives_at_company 恒为空并附"此结论可采信"文案，
			// 而公司实际有董监高名单；改用 E_007_0601（主要人员，仅需 entName）后正常返回。
			executives = s.callQuery(ctx, "E_007_0601", map[string]any{"entName": entName, "pageNo": 1, "pageSize": 20})
		}()
	}
	wg.Wait()

	out["person_search"] = jsonRaw(personSearch)
	out["enforced_records"] = jsonRaw(enforced)
	out["dishonest_records"] = jsonRaw(dishonest)
	if entName != "" {
		out["ent_name"] = entName
		out["executives_at_company"] = jsonRaw(executives)
	}
	out["api_calls"] = 3
	if entName != "" {
		out["api_calls"] = 4
	}
	out["elapsed_ms"] = time.Since(start).Milliseconds()
	// 措辞注意：person_search 是响应字段名而非工具名（曾致模型查找不存在的工具）；
	// 风险记录（被执行/失信）是"人名维度"，ent_name 不参与过滤——同名必须自行消歧。
	out["_note"] = "person_name 为唯一主体：enforced_records / dishonest_records 按人名检索，" +
		"ent_name 不参与过滤，可能包含同名他人在其他地区的记录（请结合身份证号/地区/公司自行消歧）。" +
		"executives_at_company 为指定企业的任职名单，可用于比对同名是否同一人。" +
		"（person_search 是上方结果字段，不是工具名。）"
	return mustJSON(out)
}

// EntityRiskTimeline 企业风险/变更时间线（对齐企查查 qcc-history 轻量版）
func (s *Services) EntityRiskTimeline(ctx context.Context, entName, companyID string, limit int) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}

	base := map[string]any{"pageNo": 1, "pageSize": limit}
	if entName != "" {
		base["entName"] = entName
	}
	if companyID != "" {
		base["companyId"] = companyID
	}

	type probe struct {
		key, pcode, title, paramKey string
	}
	probes := []probe{
		{"change", "E_007_0801", "工商变更", "entName"},
		{"admin_penalty", "E_007_1001", "行政处罚", "entName"},
		{"court_filing", "E_003_0351", "立案信息", "entName"},
		{"abnormal", "E_007_1201", "经营异常", "entName"},
	}

	events := make([]map[string]any, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, p := range probes {
		wg.Add(1)
		go func(pr probe) {
			defer wg.Done()
			params := map[string]any{"pageNo": 1, "pageSize": limit}
			for k, v := range base {
				params[k] = v
			}
			if pr.paramKey == "entName" && entName != "" {
				params["entName"] = entName
			}
			raw := s.callQuery(ctx, pr.pcode, params)
			parsed, ok := jsonRaw(raw).(map[string]any)
			if !ok {
				return
			}
			items := extractTimelineItems(pr.key, parsed)
			mu.Lock()
			for _, it := range items {
				ev := map[string]any{
					"source":  pr.key,
					"title":   pr.title,
					"date":    it["date"],
					"summary": it["summary"],
					"pcode":   pr.pcode,
				}
				// 工商变更带前后值：这是"变了什么"的核心信息，不能只给变更事项名。
				// 2026-09-20 去噪（实测智侒信 limit=10）：① before==after 的"无变化"事件
				// 占一半且是纯噪音，只标记不重复输出双值；② 长文本（如经营范围全文）
				// 在多事件间重复出现，一次响应可重复 6 次，截断保留首尾。
				before, hasBefore := it["before"].(string)
				after, hasAfter := it["after"].(string)
				switch {
				case hasBefore && hasAfter && before != "" && before == after:
					ev["value_unchanged"] = true
					ev["value"] = truncateTimelineValue(before)
				default:
					if hasBefore && before != "" {
						ev["before"] = truncateTimelineValue(before)
					}
					if hasAfter && after != "" {
						ev["after"] = truncateTimelineValue(after)
					}
				}
				events = append(events, ev)
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	sort.Slice(events, func(i, j int) bool {
		di, _ := events[i]["date"].(string)
		dj, _ := events[j]["date"].(string)
		return di > dj
	})
	if len(events) > limit {
		events = events[:limit]
	}

	// 日期缺失自检：抽取字段名若与上游实际不符，会静默产出空日期（实测曾 100% 为空），
	// 此处显式统计，避免时间线退化成无序列表却看不出来。
	undated := 0
	for _, e := range events {
		if s, _ := e["date"].(string); s == "" {
			undated++
		}
	}

	entity := entName
	if entity == "" {
		entity = companyID
	}
	out := map[string]any{
		"tool":        "entity_risk_timeline",
		"entity":      entity,
		"event_count": len(events),
		"timeline":    events,
		"api_calls":   len(probes),
		"_agent_meta": agentMeta(entity),
		"_note": "时间线由多接口事件抽取合并，按日期倒序；" +
			"工商变更含 before/after 前后值。深度历史请 keeprisk_query 查具体 pcode。",
	}
	if undated > 0 {
		out["undated_count"] = undated
		out["_date_note"] = fmt.Sprintf("%d/%d 条事件未取到日期（上游字段名可能与预期不符），排序对这些条目不具意义",
			undated, len(events))
	}
	return mustJSON(out)
}

// truncateTimelineValue 时间线前后值截断：超长文本（经营范围/章程等全文）保留首尾。
// 全文对时间线场景无增量信息，却会跨事件重复出现（实测占响应 59% 字节）。
func truncateTimelineValue(s string) string {
	const max = 160
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:70]) + "…[截断，原 " + fmt.Sprint(len(r)) + " 字]…" + string(r[len(r)-40:])
}

// timelineFieldMap 按事件来源显式映射"日期 / 摘要 / 前后值"的响应字段名。
//
// 为什么按来源显式映射而不是用统一候选列表（2026-09-18 教训）：
// 原实现用一条通用候选列表 firstString(m, "changeDate","penaltyDate","filingDate","putDate",...)，
// 只要某接口的真实字段名不在列表里就静默取空。实测飞友科技 limit=50 时
// **50/50 条事件日期全为空**，排序失效、limit 退化成任意截断，时间线实际不可用。
//
// 字段名依据接口文档（/接口文档pdf/markdown/企业/）与实测响应：
//
//	E_007_0801 工商变更  changeTime   变更日期      changeItem / contentBefore / contentAfter
//	E_007_1001 行政处罚  decisionDate 作出决定日期  punishNumber / content / type
//	E_003_0351 立案信息  filingDate   立案日期      caseCode / caseReason（**是 caseCode 不是 caseNo**）
//	E_007_1201 经营异常  putDate      列入日期      putReason / putDepartment
var timelineFieldMap = map[string]struct {
	DateKeys    []string
	SummaryKeys []string
	BeforeKeys  []string
	AfterKeys   []string
}{
	"change": {
		DateKeys:    []string{"changeTime", "changeDate"},
		SummaryKeys: []string{"changeItem", "changeTypeStd"},
		BeforeKeys:  []string{"contentBefore"},
		AfterKeys:   []string{"contentAfter"},
	},
	"admin_penalty": {
		DateKeys:    []string{"decisionDate", "publishDate"},
		SummaryKeys: []string{"punishNumber", "punishContent", "penaltyContent", "content", "type"},
	},
	"court_filing": {
		DateKeys:    []string{"filingDate", "startDate"},
		SummaryKeys: []string{"caseCode", "caseNo", "caseReason"},
	},
	"abnormal": {
		DateKeys:    []string{"putDate"},
		SummaryKeys: []string{"putReason", "putDepartment"},
	},
}

// extractTimelineItems 从某来源的响应中抽取时间线事件。
// source 为 timelineFieldMap 的键；未知来源退化为通用候选列表。
func extractTimelineItems(source string, parsed map[string]any) []map[string]any {
	var list []any
	if data, ok := parsed["data"].(map[string]any); ok {
		for _, k := range []string{"list", "items", "rows"} {
			if arr, ok := data[k].([]any); ok {
				list = arr
				break
			}
		}
	}
	if list == nil {
		for _, k := range []string{"list", "items", "rows"} {
			if arr, ok := parsed[k].([]any); ok {
				list = arr
				break
			}
		}
	}

	type spec struct {
		DateKeys, SummaryKeys, BeforeKeys, AfterKeys []string
	}
	sp, known := timelineFieldMap[source]
	var s spec
	if known {
		s = spec{sp.DateKeys, sp.SummaryKeys, sp.BeforeKeys, sp.AfterKeys}
	} else {
		s = spec{
			DateKeys:    []string{"changeTime", "changeDate", "penaltyDate", "decisionDate", "filingDate", "putDate", "publishDate", "regDate", "date"},
			SummaryKeys: []string{"changeItem", "penaltyContent", "caseCode", "caseNo", "putReason", "abnormalReason", "title", "content"},
		}
	}

	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		date := firstString(m, s.DateKeys...)
		summary := firstString(m, s.SummaryKeys...)
		if date == "" && summary == "" {
			continue
		}
		item := map[string]any{"date": date, "summary": summary}
		// 变更前后值：回答"变了什么"，比只给变更事项名有用得多
		if b := firstString(m, s.BeforeKeys...); b != "" {
			item["before"] = b
		}
		if a := firstString(m, s.AfterKeys...); a != "" {
			item["after"] = a
		}
		out = append(out, item)
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DomainPlaybook 六大域能力图（对齐企查查 6 MCP Server 分层）
func DomainPlaybook() []map[string]any {
	return []map[string]any{
		{
			"domain_id":   "keeprisk-company",
			"domain_name": "企业基座",
			"qcc_ref":     "qcc-company",
			"tools":       []string{"fuzzy_search", "resolve_entity", "enterprise_basic_profile", "company_quick_scan", "actual_controller", "control_penetration", "batch_query_companies"},
			"description": "工商登记、主体锚定、实控人与股权穿透",
		},
		{
			"domain_id":   "keeprisk-risk",
			"domain_name": "风控大脑",
			"qcc_ref":     "qcc-risk",
			"tools":       []string{"enterprise_credit_score", "enterprise_due_diligence_snapshot", "judicial_deep_dive", "entity_risk_timeline", "compare_enterprises"},
			"description": "失信、被执行、行政处罚、尽调快照与风险时间线",
		},
		{
			"domain_id":   "keeprisk-ipr",
			"domain_name": "知产引擎",
			"qcc_ref":     "qcc-ipr",
			"tools":       []string{"intellectual_property_portfolio", "data_badge_full"},
			"description": "专利、商标、软著、域名与知识产权角标",
		},
		{
			"domain_id":   "keeprisk-operation",
			"domain_name": "经营罗盘",
			"qcc_ref":     "qcc-operation",
			"tools":       []string{"operational_compliance", "enterprise_business_detail", "supply_chain_relations", "industrial_park_search", "search_enterprises"},
			"description": "招投标/标讯、资质许可、供应链与园区招商",
		},
		{
			"domain_id":   "keeprisk-executive",
			"domain_name": "董监高画像",
			"qcc_ref":     "qcc-executive",
			"tools":       []string{"person_risk_profile", "enterprise_deep_profile"},
			"description": "以人查风险、董监高与关联任职",
		},
		{
			"domain_id":   "keeprisk-history",
			"domain_name": "历史轨迹",
			"qcc_ref":     "qcc-history",
			"tools":       []string{"entity_risk_timeline", "annual_report_detail", "enterprise_development"},
			"description": "变更、处罚、立案等事件时间线；年报与发展历程",
		},
	}
}

