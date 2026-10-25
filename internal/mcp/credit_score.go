package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── 信用评分维度定义 ───────────────────────────

// creditSection 定义信用评分工具内部调用的每个 API 维度
type creditSection struct {
	Key         string         // 维度 key（用于内部索引）
	Pcode       string         // 接口编码
	Title       string         // 维度标题
	ParamKey    string         // 主体入参键名（entName / companyName / key）
	Category    string         // 分类：score / badge / tag
	ExtraParams map[string]any // 该接口额外的必填参数
}

// creditSections 信用评分内部聚合的 6 个 API。
//
// 2026-09-18 修正（飞友科技全量实测）：两处传参/字段名与接口文档不符，导致
// 「门卫工具给出假绿灯」——实测飞友科技（有法院公告/开庭公告）输出
// risk_level=green + "可快速通过"，而 scores 里 4 个维度有 2 个 has_data=false。
//
//   - financial_score（E_011_0251）：文档要求 `key`（企业名称或统一社会信用代码）**必填**
//     且 `year` **必填**（单年份）。旧实现传 entName 且不传 year
//     → 网关稳定返回 101/110，该维度**每次调用必然失败**。
//   - 其余评分接口的评分字段名与旧 parseScoreDimension 的候选（score/totalScore）不符，
//     实测 E_017_0111 用 entScienceInnovateScore、E_017_0121 用 entEntireScore，
//     两个维度被静默丢弃 → 综合分由唯一存活的 1 个维度算出 → 假绿灯。
var creditSections = []creditSection{
	// 评分维度（score）
	{Key: "financial_score", Pcode: "E_011_0251", Title: "企业财务状况评分", ParamKey: "key", Category: "score",
		ExtraParams: map[string]any{"year": defaultFinancialScoreYear}},
	{Key: "operational_score", Pcode: "E_011_0361", Title: "企业经营评分", ParamKey: "entName", Category: "score"},
	{Key: "innovation_score", Pcode: "E_017_0111", Title: "企业科创评分", ParamKey: "companyName", Category: "score"},
	{Key: "comprehensive_score", Pcode: "E_017_0121", Title: "企业综合评分", ParamKey: "companyName", Category: "score"},
	// 数据角标（badge）
	{Key: "data_badges", Pcode: "E_007_1391", Title: "数据角标V2", ParamKey: "companyName", Category: "badge"},
	// 企业标签（tag）
	{Key: "enterprise_tags", Pcode: "E_007_1021", Title: "企业标签", ParamKey: "entName", Category: "tag"},
}

// defaultFinancialScoreYear E_011_0251 的 year 为必填单年份。
// 取最近一个已结算年度：工商年报数据通常滞后一年（实测 2026-09 时可取到 2023 期），
// 故用「当前年 - 3」作为稳妥默认值；取不到数据时该维度按 has_data=false 处理，
// 由 buildCreditRecommendation 降级为「数据不足」而不是给绿灯。
var defaultFinancialScoreYear = func() string {
	return strconv.Itoa(time.Now().Year() - 3)
}()

// ─────────────────────────── 信用评分输出结构 ───────────────────────────

// creditScoreResult 信用评分工具的结构化输出
type creditScoreResult struct {
	Entity         string                    `json:"entity"`
	Scores         map[string]creditScoreDim `json:"scores"`
	Badges         map[string]int            `json:"badges"`
	Tags           []string                  `json:"tags"`
	RiskLevel      string                    `json:"risk_level"`
	Recommendation string                    `json:"recommendation"`
	ElapsedMs      int64                     `json:"elapsed_ms"`
	ApiCalls       int                       `json:"api_calls"`
	Errors         []string                  `json:"errors,omitempty"`
}

// creditScoreDim 单个评分维度的结果
type creditScoreDim struct {
	Score      float64 `json:"score"`
	Level      string  `json:"level"`
	Year       string  `json:"year,omitempty"`
	Title      string  `json:"title"`
	HasData    bool    `json:"has_data"`
	ScoreScale string  `json:"score_scale_note,omitempty"` // 标度归一说明（不同接口标度不一致时给出）
}

// ─────────────────────────── 核心实现 ───────────────────────────

// EnterpriseCreditScore 企业信用评分：并行聚合 6 个评分/角标/标签接口
//
// 设计要点：
//   - 作为低成本"门卫"工具（6 API calls, ~1-2s），用于快速预判信用等级
//   - 决定后续尽调深度：≥80 快速通过，50-79 标准尽调，<50 深度排查
//   - 并行调用，单接口失败不影响其他维度
func (s *Services) EnterpriseCreditScore(ctx context.Context, entName, companyID string) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	start := time.Now()
	results := make([]map[string]any, len(creditSections))
	var wg sync.WaitGroup

	for i, sec := range creditSections {
		wg.Add(1)
		go func(idx int, cs creditSection) {
			defer wg.Done()

			params := map[string]any{}
			if entName != "" {
				params[cs.ParamKey] = entName
			}
			if companyID != "" {
				params["companyId"] = companyID
			}
			// 接口声明的额外必填参数（如 E_011_0251 的 year）；主体为空时跳过，
			// 避免把 year 单独发出去（那会触发"至少填一个 key/entName"校验错误）。
			if params[cs.ParamKey] != nil || companyID != "" {
				for k, v := range cs.ExtraParams {
					params[k] = v
				}
			}

			raw := s.callQuery(ctx, cs.Pcode, params)

			item := map[string]any{
				"key":      cs.Key,
				"pcode":    cs.Pcode,
				"title":    cs.Title,
				"category": cs.Category,
			}

			var parsed map[string]any
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr == nil {
				if errStr, ok := parsed["error"].(string); ok && errStr != "" {
					item["status"] = "error"
					item["error"] = errStr
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

	// 汇总为结构化评分结果
	result := buildCreditResult(entName, results)
	result.ElapsedMs = time.Since(start).Milliseconds()
	result.ApiCalls = len(creditSections)

	return mustJSON(result)
}

// buildCreditResult 将 6 个 API 的原始响应汇总为结构化信用评分
func buildCreditResult(entName string, results []map[string]any) creditScoreResult {
	r := creditScoreResult{
		Entity:  entName,
		Scores:  make(map[string]creditScoreDim),
		Badges:  make(map[string]int),
		Errors:  nil,
	}

	var hasRedFlag bool
	var scoreCount int
	var scoreSum float64

	for _, item := range results {
		key, _ := item["key"].(string)
		status, _ := item["status"].(string)
		category, _ := item["category"].(string)

		if status != "ok" {
			errMsg := fmt.Sprintf("%s: %v", key, item["error"])
			r.Errors = append(r.Errors, errMsg)
			continue
		}

		data, ok := item["data"].(map[string]any)
		if !ok {
			continue
		}

		switch category {
		case "score":
			dim := parseScoreDimension(key, item["title"].(string), data)
			r.Scores[key] = dim
			if dim.HasData {
				scoreCount++
				scoreSum += dim.Score
				if dim.Score < 50 {
					hasRedFlag = true
				}
			}

		case "badge":
			r.Badges = parseBadges(data)

		case "tag":
			r.Tags = parseTags(data)
		}
	}

	// 综合评分：有数据时取均值，否则 0
	if scoreCount > 0 {
		avg := scoreSum / float64(scoreCount)
		r.Scores["composite"] = creditScoreDim{
			Score:   roundToOneDecimal(avg),
			Level:   scoreToLevel(avg),
			Title:   "综合评分",
			HasData: true,
		}
	}

	// 风险等级与决策建议
	r.RiskLevel, r.Recommendation = buildCreditRecommendation(r.Scores, r.Badges, hasRedFlag)

	return r
}

// scoreFieldCandidates 各评分接口的评分字段名与标度。
//
// 实测（2026-09-18 飞友科技，逐接口取原始响应核对）：
//   - E_011_0361 企业经营评分：`score: 95.35`（0–100）
//   - E_011_0251 企业财务状况评分：`Score`（PascalCase，0–100）
//   - E_017_0111 企业科创评分：`entScienceInnovateScore: 95.11`（0–100）
//   - E_017_0121 企业综合评分：`entEntireScore: 852.88`（**0–1000 标度**，
//     同响应的 industryEntEntireScoreAvg=536.99 / areaEntEntireScoreAvg=542.72 同为该标度，
//     故需除以 10 归一到 0–100，否则综合分会把 0–100 的维度整体抬高约 2 倍）。
//
// `entDevelop` 刻意不列入：它在 E_017_0111 为 55.5、在 E_017_0121 为 703.4，
// 两接口标度不一致，取值会让综合分跨接口不可比。
var scoreFieldCandidates = []struct {
	Name  string
	Scale float64 // 原始值 → 0–100 的除数
}{
	{"score", 1},
	{"Score", 1},
	{"totalScore", 1},
	{"TotalScore", 1},
	{"entScienceInnovateScore", 1},
	{"entEntireScore", 10},
}

// parseScoreDimension 从评分 API 响应中提取评分维度
//
// 响应结构（通用）：
//
//	{ "code": 100, "data": { "score": 78, "level": "B+", "year": "2024" } }
//	或 { "code": 100, "data": [{ "score": 78, "level": "B+", "year": "2024" }] }
func parseScoreDimension(key, title string, parsed map[string]any) creditScoreDim {
	dim := creditScoreDim{Title: title}

	// 尝试从 data 字段提取
	var scoreData map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		scoreData = d
	} else if arr, ok := parsed["data"].([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			scoreData = m
		}
	}
	if scoreData == nil {
		// 也尝试顶层字段
		scoreData = parsed
	}

	// 按实测候选逐个尝试（含标度归一）
	for _, c := range scoreFieldCandidates {
		var raw float64
		found := false
		switch v := scoreData[c.Name].(type) {
		case float64:
			raw, found = v, true
		case string:
			var f float64
			if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
				raw, found = f, true
			}
		}
		if !found {
			continue
		}
		scale := c.Scale
		// 标度自适应：同一字段上游存在两种量纲——文档示例 entEntireScore=99.99（0–100），
		// 实测飞友科技=852.88、行业均值=536.99（0–1000）。无法从响应判知标度声明，
		// 故以 200 为界：超过则按 0–1000 归一，否则原样。
		if c.Scale == 10 && raw > 0 && raw <= 200 {
			scale = 1
		}
		dim.Score = raw / scale
		dim.HasData = true
		if scale != 1 {
			dim.ScoreScale = fmt.Sprintf("原始值 %.2f（%s，疑似 0–1000 标度）已归一到 0–100",
				raw, c.Name)
		}
		break
	}

	// 提取 level
	if l, ok := scoreData["level"].(string); ok {
		dim.Level = l
	} else {
		dim.Level = scoreToLevel(dim.Score)
	}

	// 提取 year（财务评分特有）
	if y, ok := scoreData["year"].(string); ok {
		dim.Year = y
	} else if y, ok := scoreData["Year"].(string); ok {
		dim.Year = y
	} else if y, ok := scoreData["year"].(float64); ok {
		dim.Year = fmt.Sprintf("%.0f", y)
	}

	return dim
}

// parseBadges 从数据角标 V2 响应中提取分类计数（支持扁平与嵌套两种结构）。
//
// 实测结构（E_007_1391，2026-09-18）是按业务域**嵌套**的计数字典：
//
//	{"bussiness":{"companyChangeInfoTotal":75,...},
//	 "intellectual":{"brandCount":37,"copyrightRegCount":73,"entPatentInfoCount":22},
//	 "judicature":{"courtTotal":2,"announcementTotal":8,"enforcementTotal":0,...},
//	 "management":{...}, "operationalRisk":{...}, "development":{...}}
//
// 旧实现只认扁平的 judicialCount/businessCount/... 十个键，对嵌套结构**一个都不命中**
// → badges 恒为空 → 司法红旗检测（buildCreditRecommendation 里的 judicialBadges）恒不触发。
// 这是「门卫工具给假绿灯」的四个叠加缺陷之一：实测飞友科技有法院公告 2 条 + 开庭公告 8 条，
// 却因为角标解析不出来而输出 risk_level=green。
//
// 现值：返回扁平化的「叶子计数 + 每个业务域的域级合计」，域级键沿用信用评分既有语义名
// （judicial/ip/risk/business/operation），便于 buildCreditRecommendation 直接使用。
func parseBadges(parsed map[string]any) map[string]int {
	badges := make(map[string]int)

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	} else {
		data = parsed
	}
	if data == nil {
		return badges
	}

	// 扁平键（部分接口/历史形态）
	flatKeys := map[string]string{
		"judicialCount": "judicial", "businessCount": "business", "operationCount": "operation",
		"ipCount": "ip", "riskCount": "risk",
		"judicial_count": "judicial", "business_count": "business", "operation_count": "operation",
		"ip_count": "ip", "risk_count": "risk",
	}
	for srcKey, dstKey := range flatKeys {
		if v, ok := anyToInt(data[srcKey]); ok {
			badges[dstKey] = v
		}
	}

	// 嵌套键：业务域 → 信用评分语义
	domainAlias := map[string]string{
		"judicature": "judicial", "judicial": "judicial",
		"intellectual": "ip", "ip": "ip", "ipr": "ip",
		"operationalRisk": "risk", "risk": "risk",
		"bussiness": "business", "business": "business",
		"management": "operation", "operation": "operation",
		"development": "development",
	}
	for groupKey, alias := range domainAlias {
		group, ok := data[groupKey].(map[string]any)
		if !ok {
			continue
		}
		sum := 0
		for leaf, v := range group {
			n, ok := anyToInt(v)
			if !ok {
				continue
			}
			sum += n
			// 叶子计数原名保留，便于下游定位是哪个子项（如 courtTotal/announcementTotal）
			if _, exists := badges[leaf]; !exists {
				badges[leaf] = n
			}
		}
		badges[alias] = sum
	}

	// 兜底：都没有命中时取 total
	if len(badges) == 0 {
		if t, ok := anyToInt(data["total"]); ok {
			badges["total"] = t
		}
	}
	return badges
}

// parseNestedBadges 保留角标的业务域分组结构，供聚合 digest 展示（比扁平化更易读）。
// 只保留非零计数，避免把 100+ 个零值字段灌进上下文。
func parseNestedBadges(parsed map[string]any) map[string]any {
	out := map[string]any{}

	var data map[string]any
	if d, ok := parsed["data"].(map[string]any); ok {
		data = d
	} else {
		data = parsed
	}
	if data == nil {
		return out
	}

	// 嵌套形态
	for groupKey, v := range data {
		group, ok := v.(map[string]any)
		if !ok {
			continue
		}
		nonzero := map[string]any{}
		for leaf, lv := range group {
			if n, ok := anyToInt(lv); ok && n != 0 {
				nonzero[leaf] = n
			}
		}
		if len(nonzero) > 0 {
			out[groupKey] = nonzero
		}
	}
	if len(out) > 0 {
		return out
	}

	// 扁平兜底
	for k, v := range data {
		if n, ok := anyToInt(v); ok && n != 0 {
			out[k] = n
		}
	}
	return out
}

// parseTags 从企业标签响应中提取标签列表
//
// 响应结构（E_007_1021）：
//
//	{ "code": 100, "data": { "list": [{"tagName": "高新技术企业"}, ...] } }
//	或 { "code": 100, "data": ["高新技术企业", "规上企业"] }
func parseTags(parsed map[string]any) []string {
	var tags []string

	var data any
	if d, ok := parsed["data"]; ok {
		data = d
	} else {
		data = parsed
	}

	// 情况 1：data 是数组，每个元素可能是 string 或 map
	if arr, ok := data.([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				tags = append(tags, s)
			} else if m, ok := item.(map[string]any); ok {
				if t, ok := m["tagName"].(string); ok && t != "" {
					tags = append(tags, t)
				} else if t, ok := m["name"].(string); ok && t != "" {
					tags = append(tags, t)
				} else if t, ok := m["label"].(string); ok && t != "" {
					tags = append(tags, t)
				}
			}
		}
		return tags
	}

	// 情况 2：data 是 map，内部有 list 或 tags 数组
	if m, ok := data.(map[string]any); ok {
		if list, ok := m["list"].([]any); ok {
			for _, item := range list {
				if im, ok := item.(map[string]any); ok {
					if t, ok := im["tagName"].(string); ok && t != "" {
						tags = append(tags, t)
					} else if t, ok := im["name"].(string); ok && t != "" {
						tags = append(tags, t)
					}
				}
			}
		} else if tagArr, ok := m["tags"].([]any); ok {
			for _, item := range tagArr {
				if s, ok := item.(string); ok && s != "" {
					tags = append(tags, s)
				}
			}
		}
		// 情况 3：扁平"状态位"字典（E_007_1021 企业标签的真实形态）。
		//
		// 实测响应是一大票 *_State / *Label 布尔位（如 aTaxpayerEntState:1、
		// aboveGaugeState:1、highEntState:1、courtAnnouncementNum:0…），既无 list
		// 也无 tags 数组，旧实现在这里返回 nil → 企业标签恒为空。
		// 现将**取值为真**的状态位翻成可读标签（key 去掉 State/EntState/StateName 后缀）。
		if len(tags) == 0 {
			tags = extractStateTags(m)
		}
	}

	return tags
}

// extractStateTags 把 E_007_1021 的扁平状态位字典翻成标签列表。
// 只收"取值为真"的状态位，且跳过计数类字段（*Num / *Total / *Rate / *Rank），
// 避免把 "courtAnnouncementNum: 0" 之类当成标签。
func extractStateTags(m map[string]any) []string {
	var tags []string
	for k, v := range m {
		if !isStateKey(k) {
			continue
		}
		on := false
		switch t := v.(type) {
		case float64:
			on = t == 1
		case int:
			on = t == 1
		case bool:
			on = t
		}
		if on {
			tags = append(tags, stateKeyToLabel(k))
		}
	}
	sort.Strings(tags)
	return tags
}

// stateKeyToLabel 去掉状态位后缀，得到可读标签名。
func stateKeyToLabel(k string) string {
	for _, suf := range []string{"EntState", "StateName", "State", "Label"} {
		if strings.HasSuffix(k, suf) && len(k) > len(suf) {
			return k[:len(k)-len(suf)]
		}
	}
	return k
}

// isStateKey 判断是否状态位字段（排除计数/比率/排名类）。
func isStateKey(k string) bool {
	for _, suf := range []string{"Num", "Total", "Rate", "Rank", "Count", "Time", "Name", "Code", "Label"} {
		if strings.HasSuffix(k, suf) && suf != "Label" {
			return false
		}
	}
	for _, suf := range []string{"State", "StateName", "Label"} {
		if strings.HasSuffix(k, suf) {
			return true
		}
	}
	return false
}

// buildCreditRecommendation 基于评分和角标生成风险等级与决策建议。
//
// 2026-09-18 加固（飞友科技实测复现「假绿灯」）：
// 旧实现在**角标解析失败 / 评分维度大量缺失**时仍会走 `composite >= 80 → green`
// 并输出"可快速通过（轻量核验即可）"。实测飞友科技（法院公告 2 条 + 开庭公告 8 条）
// 因角标解析不出、4 个评分维度只剩 1 个存活，综合分由单个维度算出 → 输出 green。
//
// 本工具是全工具集中**唯一直接给行动建议**的字段，因此加了「证据完整性闸门」：
//   - 角标完全取不到（badges 为空）→ 无法判断司法/经营风险，不得给通过建议
//   - 存活的评分维度少于 2 个 → 综合分不具代表性，不得给通过建议
// 两者命中都降级为 unknown 并明确说明缺什么，而不是给一个看起来确定的绿灯。
func buildCreditRecommendation(scores map[string]creditScoreDim, badges map[string]int, hasRedFlag bool) (level, recommendation string) {
	composite, hasComposite := scores["composite"]

	// 角标中有无红色信号（司法角标 > 5 或风险角标 > 0）
	judicialBadges := badges["judicial"] + badges["risk"]
	hasJudicialSignal := judicialBadges > 5 || badges["risk"] > 0

	// 证据完整性：角标是司法/经营风险的唯一来源，取不到就不能判断
	badgesKnown := len(badges) > 0
	// 存活的评分维度数（composite 是派生项，不计入）
	surviving := 0
	for k, d := range scores {
		if k == "composite" {
			continue
		}
		if d.HasData {
			surviving++
		}
	}

	switch {
	case hasRedFlag || hasJudicialSignal:
		level = "red"
		recommendation = fmt.Sprintf("综合评分 %.0f，存在红色信号，建议深度排查（全量尽调+司法穿透）", composite.Score)
	case !badgesKnown:
		level = "unknown"
		recommendation = "角标数据未取到，无法判断司法/经营风险，不建议据此通过；请改用 enterprise_due_diligence_snapshot 核验后再决策"
	case surviving < 2:
		level = "unknown"
		recommendation = fmt.Sprintf("仅 %d 个评分维度取到数据，综合分不具代表性，不建议据此通过；请走标准尽调流程", surviving)
	case hasComposite && composite.Score < 60:
		level = "yellow"
		recommendation = fmt.Sprintf("综合评分 %.0f，评分偏低，建议标准尽调流程", composite.Score)
	case hasComposite && composite.Score >= 80:
		level = "green"
		recommendation = fmt.Sprintf("综合评分 %.0f（基于 %d 个评分维度 + 角标），评分口径内信用良好；"+
			"本建议不覆盖法院公告/开庭公告/被执行等司法维度，涉及授信/准入请另做司法排查", composite.Score, surviving)
	case hasComposite:
		level = "yellow"
		recommendation = fmt.Sprintf("综合评分 %.0f，建议标准尽调流程", composite.Score)
	default:
		level = "unknown"
		recommendation = "评分数据不足，建议走标准尽调流程确认"
	}
	return
}

// ─────────────────────────── 工具函数 ───────────────────────────

// scoreToLevel 将数值评分映射为等级
func scoreToLevel(score float64) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B+"
	case score >= 70:
		return "B"
	case score >= 60:
		return "C"
	case score >= 50:
		return "D"
	case score > 0:
		return "E"
	default:
		return ""
	}
}

// roundToOneDecimal 四舍五入到一位小数
func roundToOneDecimal(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
