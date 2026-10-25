package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/quota"
)

type ddSection struct {
	Key   string `json:"key"`
	Pcode string `json:"pcode"`
	Title string `json:"title"`
	// Judicial 标记；include_judicial=false 时整组跳过
	Judicial bool `json:"-"`
	// Risk 标记经营风险类（行政处罚/经营异常等），不受 include_judicial 控制
	Risk bool `json:"-"`
	// ParamKey 该节入参主体键名（entName/name/text/companyName/keyword），根据 PDF 精确对齐
	ParamKey string `json:"-"`
	// ExtraParams 除主体外的固定附加入参（如 E_003_0311 的 idNo 可选）
	ExtraParams func(entName, companyID string) map[string]any `json:"-"`
	// PageSize 该节推荐 pageSize（裁判文书每条很长，给 3；失信/被执行给 5）
	PageSize int `json:"-"`
}

// 企业尽调/风险扫描常见维度（一次工具多接口聚合）
//
// 计费提醒：每个 section 都是一次独立的网关调用。调用方应按业务需要选择 section 集合，
// 不要默认全开（避免尽调成本失控）。
//
// 入参键名对齐：每个接口的"主体字段"不同，必须按官方 PDF 文档精确区分：
//   - entName：工商/股东/人员/立案/严重失信/限高/行政处罚/经营异常/严重违法/清算信息/欠税公告/法院公告/开庭公告/司法拍卖/司法协助
//   - name：终本/被执行/失信/破产重整/税务违法
//   - text：裁判文书
//   - companyName：供应商与客户/产品质量抽查
//   - keyword：舆情检索
//
// 分组说明：
//   - Judicial=true 的 section 受 include_judicial 开关控制（默认全开，可关闭节省计费）
//   - Risk=true 的 section 是经营风险类（行政处罚/经营异常等），不受 include_judicial 控制
//   - 两者都不标记的 section（basic/shareholders/key_personnel）始终默认执行
var dueDiligenceSections = []ddSection{
	// 基础工商（始终执行）
	{Key: "basic", Pcode: "E_007_0501", Title: "工商基本信息", ParamKey: "entName", PageSize: 1},
	{Key: "shareholders", Pcode: "E_007_0901", Title: "工商股东", ParamKey: "entName", PageSize: 5},
	// E_007_0601 的 PageSize 必须与 person_risk_profile / enterprise_extended_profile
	// 的调用点一致（20）：缓存 key 含 pageSize，不一致会导致同主体同接口重复计费（实测一回合 3 次）。
	{Key: "key_personnel", Pcode: "E_007_0601", Title: "主要人员", ParamKey: "entName", PageSize: 20},

	// 司法风险（include_judicial=false 时跳过）
	{Key: "court_filing", Pcode: "E_003_0351", Title: "立案信息", Judicial: true, ParamKey: "entName", PageSize: 5},
	{Key: "zhongben", Pcode: "E_003_0311", Title: "终本案件", Judicial: true, ParamKey: "name", PageSize: 5},
	{Key: "judgment", Pcode: "E_003_0402", Title: "裁判文书", Judicial: true, ParamKey: "text", PageSize: 3},
	{Key: "enforced", Pcode: "E_003_0702", Title: "被执行人V2", Judicial: true, ParamKey: "name", PageSize: 5},
	{Key: "dishonest", Pcode: "E_003_0802", Title: "失信人V2", Judicial: true, ParamKey: "name", PageSize: 5},
	{Key: "restraint", Pcode: "P_003_0361", Title: "限制高消费", Judicial: true, ParamKey: "entName", PageSize: 5},
	{Key: "serious_dishonesty", Pcode: "E_003_0411", Title: "信用中国_严重失信", Judicial: true, ParamKey: "entName", PageSize: 5},

	// 经营风险（供应链/授信必查，不受 include_judicial 控制）
	{Key: "admin_penalty", Pcode: "E_007_1001", Title: "行政处罚", Risk: true, ParamKey: "entName", PageSize: 5},
	{Key: "abnormal_operation", Pcode: "E_007_1201", Title: "经营异常", Risk: true, ParamKey: "entName", PageSize: 5},
	{Key: "serious_illegal", Pcode: "E_007_1401", Title: "严重违法", Risk: true, ParamKey: "entName", PageSize: 5},
	{Key: "liquidation", Pcode: "E_007_1501", Title: "清算信息", Risk: true, ParamKey: "entName", PageSize: 5},
	{Key: "tax_violation", Pcode: "E_008_0041", Title: "税务违法", Risk: true, ParamKey: "name", PageSize: 5},
	{Key: "tax_arrears", Pcode: "E_008_0001", Title: "欠税公告", Risk: true, ParamKey: "entName", PageSize: 5},

	// 高风险司法信号（破产/拍卖，供应链/授信 red flag）
	{Key: "court_announcement", Pcode: "E_003_0601", Title: "法院公告", Judicial: true, ParamKey: "entName", PageSize: 5},
	{Key: "judicial_auction", Pcode: "E_003_1501", Title: "司法拍卖", Judicial: true, ParamKey: "entName", PageSize: 5},
	{Key: "bankruptcy", Pcode: "E_003_1811", Title: "破产重整", Judicial: true, ParamKey: "name", PageSize: 5},
	{Key: "court_hearing", Pcode: "E_003_0901", Title: "开庭公告", Judicial: true, ParamKey: "entName", PageSize: 5},
	{Key: "judicial_assist", Pcode: "E_007_2301", Title: "司法协助", Judicial: true, ParamKey: "entName", PageSize: 5},

	// 授信/金融维度（银行授信 + 供应链金融必查）
	{Key: "equity_pledge", Pcode: "E_007_2101", Title: "股权出质", Risk: true, ParamKey: "entName", PageSize: 5},
	{Key: "chattel_mortgage", Pcode: "E_007_1701", Title: "动产抵押", Risk: true, ParamKey: "entName", PageSize: 5},

	// 税务维度
	{Key: "tax_rating", Pcode: "E_011_0401", Title: "税务评级", Risk: false, ParamKey: "name", PageSize: 1},

	// 舆情维度（实时数据，不缓存）
	{Key: "public_sentiment", Pcode: "E_009_0101", Title: "舆情检索", ParamKey: "keyword", PageSize: 5},

	// UBO 维度（最终受益人，始终执行）
	{Key: "beneficial_owner", Pcode: "E_007_0301", Title: "最终受益人", ParamKey: "entName", PageSize: 5},
}

var ddSectionIndex = func() map[string]ddSection {
	m := make(map[string]ddSection, len(dueDiligenceSections))
	for _, s := range dueDiligenceSections {
		m[s.Key] = s
	}
	return m
}()

// sectionDigest 每个尽调维度的结构化风险摘要（减少 Agent token 消耗）
type sectionDigest struct {
	HasRecords bool   `json:"has_records"`
	Count      int    `json:"count"`
	Severity   string `json:"severity"`    // none / low / medium / high / critical
	RiskLevel  string `json:"risk_level"`  // green / yellow / red
	Summary    string `json:"summary"`
}

// generateDigest 根据 section key 和已解析的网关响应生成结构化摘要
func generateDigest(key string, parsed map[string]any) sectionDigest {
	d := sectionDigest{Severity: "none", RiskLevel: "green"}

	// 通用：尝试从响应中提取记录数
	count := extractCount(parsed)

	switch key {
	// ── 基础工商 ──
	case "basic":
		// 网关信封结构为 {code, message, data: {...}}，企业字段在 data 下；
		// 兼容 data 为单对象或单元素数组两种形态，顶层取值为兜底。
		src := parsed
		if data, ok := parsed["data"]; ok {
			switch dv := data.(type) {
			case map[string]any:
				src = dv
			case []any:
				if len(dv) > 0 {
					if m, ok := dv[0].(map[string]any); ok {
						src = m
					}
				}
			}
		}
		status, _ := src["entStatus"].(string)
		entName, _ := src["entName"].(string)
		legal, _ := src["legalName"].(string)
		if entName == "" {
			entName, _ = src["creditCode"].(string)
		}
		// 无任何企业字段（如 code=110 无记录/空数据）时不得声称「有记录+绿灯」，
		// 否则会向 Agent 注入「企业存在且低风险」的幻觉（线上实测曾复现）。
		if entName == "" && legal == "" && status == "" {
			d.HasRecords = false
			d.Count = 0
			d.Summary = "网关未返回企业字段（code=110 无记录或空数据），不能证明企业存在；请先用 resolve_entity 确认主体"
		} else {
			d.HasRecords = true
			d.Summary = fmt.Sprintf("企业=%s 法人=%s 状态=%s", entName, legal, status)
		}

	case "shareholders":
		d.Count = count
		d.HasRecords = count > 0
		d.Summary = fmt.Sprintf("股东 %d 人", count)

	case "key_personnel":
		d.Count = count
		d.HasRecords = count > 0
		d.Summary = fmt.Sprintf("主要人员 %d 人", count)

	// ── 司法维度：有记录即 high/critical ──
	case "court_filing":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("立案信息 %d 条", count)

	case "zhongben":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "critical"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("终本案件 %d 条", count)

	case "judgment":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		}
		d.Summary = fmt.Sprintf("裁判文书 %d 条", count)

	case "enforced":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("被执行人记录 %d 条", count)

	case "dishonest":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "critical"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("失信人记录 %d 条", count)

	case "restraint":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("限制高消费 %d 条", count)

	case "serious_dishonesty":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "critical"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("严重失信 %d 条", count)

	case "court_announcement":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("法院公告 %d 条", count)

	case "judicial_auction":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("司法拍卖 %d 条", count)

	case "bankruptcy":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "critical"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("破产重整 %d 条", count)

	case "court_hearing":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		}
		d.Summary = fmt.Sprintf("开庭公告 %d 条", count)

	case "judicial_assist":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("司法协助 %d 条", count)

	// ── 经营风险 ──
	case "admin_penalty":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("行政处罚 %d 条", count)

	case "abnormal_operation":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("经营异常 %d 条", count)

	case "serious_illegal":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "critical"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("严重违法 %d 条", count)

	case "liquidation":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("清算信息 %d 条", count)

	case "tax_violation":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("税务违法 %d 条", count)

	case "tax_arrears":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "high"
			d.RiskLevel = "red"
		}
		d.Summary = fmt.Sprintf("欠税公告 %d 条", count)

	// ── 授信/金融 ──
	case "equity_pledge":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		}
		d.Summary = fmt.Sprintf("股权出质 %d 条", count)

	case "chattel_mortgage":
		d.Count = count
		d.HasRecords = count > 0
		if count > 0 {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		}
		d.Summary = fmt.Sprintf("动产抵押 %d 条", count)

	// ── 税务评级 ──
	case "tax_rating":
		d.Count = count
		rating := extractTaxRating(parsed)
		d.HasRecords = rating != ""
		if rating == "D" {
			d.Severity = "high"
			d.RiskLevel = "red"
		} else if rating == "C" {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		} else if rating != "" {
			d.Severity = "low"
			d.RiskLevel = "green"
		}
		d.Summary = fmt.Sprintf("税务评级=%s", rating)

	// ── 舆情 ──
	case "public_sentiment":
		d.Count = count
		d.HasRecords = count > 0
		if count > 5 {
			d.Severity = "medium"
			d.RiskLevel = "yellow"
		} else if count > 0 {
			d.Severity = "low"
			d.RiskLevel = "green"
		}
		d.Summary = fmt.Sprintf("舆情 %d 条", count)

	// ── UBO ──
	case "beneficial_owner":
		d.Count = count
		d.HasRecords = count > 0
		names := extractBeneficialNames(parsed)
		if names != "" {
			d.Summary = fmt.Sprintf("最终受益人: %s", names)
		} else {
			d.Summary = fmt.Sprintf("最终受益人 %d 条", count)
		}

	default:
		d.Count = count
		d.HasRecords = count > 0
		d.Summary = fmt.Sprintf("%d 条记录", count)
	}
	return d
}

// extractCount 提取记录数（**优先取接口声明的数据总量 total**，无 total 时退化为当页条数）。
//
// 口径说明（2026-09-18 明确，依据接口文档）：
//   - `total` = 数据总量；`items`/`list` = **单页**明细，两者不等属正常分页
//     （官方示例 E_007_0201 即 total=268 配 10 条 items）。
//   - digest 的 record_count 取 total，才能让调用方看到真实规模：
//     若取当页条数，飞友科技的 345 件商标会被摘要成"10 条"，属严重低估。
//   - 需要"当页实际返回几条"时用 extractPageCount，两者都应出现在 digest 里。
func extractCount(parsed map[string]any) int {
	if n, ok := extractTotal(parsed); ok {
		return n
	}
	return extractPageCount(parsed)
}

// extractPageCount 提取**当页**实际返回的记录条数（取最大的载荷数组长度）。
func extractPageCount(parsed map[string]any) int {
	best := 0
	// data 直接是数组的形态（部分接口把载荷裸放在 data）
	if arr, ok := parsed["data"].([]any); ok {
		best = len(arr)
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		for _, k := range countArrayKeys {
			if arr, ok := data[k].([]any); ok && len(arr) > best {
				best = len(arr)
			}
		}
	}
	for _, k := range countArrayKeys {
		if arr, ok := parsed[k].([]any); ok && len(arr) > best {
			best = len(arr)
		}
	}
	return best
}

// countArrayKeys 记录型载荷的数组键（实测归纳自 236 接口，含图类与产业链类）。
var countArrayKeys = []string{
	"list", "items", "rows", "records", "result",
	"nodes", "links", "edges", "relationships",
	"chainList", "nodeList",
}

// extractTotal 提取接口声明的"数据总数"（权威口径）。第二个返回值为 false 表示该接口不返回 total。
func extractTotal(parsed map[string]any) (int, bool) {
	for _, k := range []string{"total", "totalNum", "total_count", "totalCount"} {
		if v, ok := parsed[k]; ok {
			if n, ok := anyToInt(v); ok {
				return n, true
			}
		}
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		for _, k := range []string{"total", "totalNum", "total_count", "totalCount"} {
			if v, ok := data[k]; ok {
				if n, ok := anyToInt(v); ok {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// digestSummary 统一生成摘要文本。
//
// 存在的必要性：早期各 digest 直接写 `fmt.Sprintf("%s: %d 条记录", key, count)`，
// 对"有数据但接口不提供条数口径"的载荷（扁平指标对象、区间金额对象、角标计数对象等）
// 会输出 "0 条记录"，与同一 digest 里 has_data=true **自相矛盾**，
// 实测于 annual_report_investments.financial_annual（E_007_0931 返回 8 个区间字段，
// 既无 total 也无数组 → count=0）。
func digestSummary(key string, count, pageCount int, hasData bool) string {
	switch {
	case !hasData:
		return key + ": 无数据"
	case pageCount > 0:
		return fmt.Sprintf("%s: %d 条（本页返回 %d 条）", key, count, pageCount)
	case count > 0:
		return fmt.Sprintf("%s: %d 条记录", key, count)
	default:
		// 有业务载荷，但该接口以对象而非记录列表表达（无条数概念）
		return key + ": 有数据（该接口以指标对象返回，无记录条数）"
	}
}

// hasAnyData 判断网关响应是否含业务载荷，供各 digest 的 has_data 字段统一使用。//
// 直接复用 isEmptyResult 的证据判定（query.go），避免各处再枚举"什么算数据"——
// 旧实现正是按各接口形状逐个枚举数组键，导致图类（nodes/links）、角标类（扁平数值对象）、
// 比值类（assetLiRate 等）全部漏判为 has_data=false。
// 实测（飞友科技）：A2 类误报 20 处，涉及 data_badge_full 全 4 节、
// enterprise_graph_relations 的 enterprise_graph/substantive_graph、
// investment_analysis_suite 的 financial_health 等。
func hasAnyData(parsed map[string]any) bool {
	return hasPayloadEvidence(parsed, 0)
}

// enrichDigestPaging 为 digest 补齐分页口径字段：total（数据总量）与 page_count（当页条数）。
//
// 为什么放在聚合引擎统一做、而不是各 build*Digest 内各自实现（2026-09-18）：
// 一次性为 3 个 digest 补过该逻辑，但知产/产业链/评价等 5+ 个构造器随即漏掉——
// 实测知识产权组合的 5 个节 record_count=总量（320/92/37/345）却既无 total 也无 page_count，
// 调用方看不到"当页只有 10 条"。集中补一次可保证所有聚合工具口径一致、后续新增不再漏。
//
// 口径依据接口文档：`total` = 数据总量，`items`/`list` = 单页明细，两者不等属正常分页。
func enrichDigestPaging(parsed map[string]any, digest map[string]any) {
	if digest == nil {
		return
	}
	// 图类 digest（含 node_count）的 record_count 是"节点+边"合计，
	// "本页 N 条"的表述对它不成立，跳过 page_count 与摘要改写。
	if _, isGraph := digest["node_count"]; isGraph {
		if _, has := digest["total"]; !has {
			if total, ok := extractTotal(parsed); ok {
				digest["total"] = total
			}
		}
		return
	}
	rc, _ := anyToInt(digest["record_count"])
	if total, ok := extractTotal(parsed); ok {
		if _, has := digest["total"]; !has {
			digest["total"] = total
		}
		if rc == 0 {
			digest["record_count"] = total
		}
	}
	pc := extractPageCount(parsed)
	if pc > 0 {
		effRC, _ := anyToInt(digest["record_count"])
		if pc != effRC {
			digest["page_count"] = pc
			// 摘要里点明"数据总量 vs 本页条数"，避免把单页读成全部
			if _, has := digest["summary"]; has {
				if key, ok := digest["summary"].(string); ok {
					if i := indexByte(key, ':'); i > 0 {
						digest["summary"] = fmt.Sprintf("%s %d 条（本页返回 %d 条）",
							key[:i], effRC, pc)
					}
				}
			}
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// extractTaxRating 从税务评级响应中提取评级值（A/B/C/D/M）。
//
// 实测响应形态（E_011_0401，2026-09-18 飞友科技）：
//
//	{"data": {"list": [{"grade": "A", "year": "2025", "evalDepartment": "国家税务总局安徽省税务局",
//	                    "idNumber": "913401007810805327", "name": "..."}], "total": 4}}
//
// 旧实现只找 data.taxRating / 顶层 taxRating —— 两个都不存在，评级恒为空串，
// 于是 digest 报 has_records=false、summary 输出"税务评级="（空值），
// 而实际该企业有 A 级评级（本次实测）。
func extractTaxRating(parsed map[string]any) string {
	if r, ok := parsed["taxRating"].(string); ok && r != "" {
		return r
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		return ""
	}
	if r, ok := data["taxRating"].(string); ok && r != "" {
		return r
	}
	// 主形态：data.list[].grade（按 year 取最新一条）
	if list, ok := data["list"].([]any); ok {
		best, bestYear := "", ""
		for _, it := range list {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			g, _ := m["grade"].(string)
			if g == "" {
				continue
			}
			y := fmt.Sprintf("%v", m["year"])
			if y == "<nil>" {
				y = ""
			}
			if best == "" || y > bestYear {
				best, bestYear = g, y
			}
		}
		return best
	}
	// 兜底：data 本身是单条记录
	if g, ok := data["grade"].(string); ok && g != "" {
		return g
	}
	return ""
}

// extractBeneficialNames 从最终受益人响应中提取姓名列表
func extractBeneficialNames(parsed map[string]any) string {
	var names []string
	tryExtract := func(arr []any) {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if n, ok := m["name"].(string); ok && n != "" {
					names = append(names, n)
				}
			}
		}
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		for _, k := range []string{"list", "items", "rows"} {
			if arr, ok := data[k].([]any); ok {
				tryExtract(arr)
				break
			}
		}
	} else if arr, ok := parsed["data"].([]any); ok {
		tryExtract(arr)
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ",") + fmt.Sprintf("等%d人", len(names))
	}
	return strings.Join(names, ",")
}

// DueDiligenceSnapshot 并行聚合企业尽调快照
//
// 参数：
//   - sections：逗号分隔的节 key 列表（可选）；空则按 include_judicial 决定
//   - includeJudicial：master 开关；为 false 时跳过所有 Judicial=true 的节
//   - maxConcurrency：并发上限（默认 4，1-9）
//   - digestOnly：为 true 时每节只返回 key/title/digest/status，省略 data（大幅减少 token）
//
// 设计要点：
//   - 各节独立网关调用，并行执行，受 maxConcurrency 信号量限制
//   - 单节失败不影响其他节，结果中带 status=ok/error + error 字段
//   - 返回 billing_units：实际产生计费的节数
func (s *Services) DueDiligenceSnapshot(ctx context.Context, entName, companyID, sectionsParam string, includeJudicial bool, maxConcurrency int, digestOnly bool) string {
	entName = strings.TrimSpace(entName)
	companyID = strings.TrimSpace(companyID)
	if entName == "" && companyID == "" {
		return mustJSON(map[string]any{"error": "请提供 ent_name 或 company_id"})
	}

	// 选择需要执行的 section 列表
	selected, skipped, validationErr := selectSections(sectionsParam, includeJudicial)
	if validationErr != nil {
		return mustJSON(validationErr)
	}

	if maxConcurrency < 1 {
		maxConcurrency = 1
	}
	if maxConcurrency > 9 {
		maxConcurrency = 9
	}

	start := time.Now()
	results := make([]map[string]any, len(selected))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i, sec := range selected {
		wg.Add(1)
		go func(idx int, s2 ddSection) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 按节定义构造入参：按 ParamKey 写入主体；按 ExtraParams 写入附加参数
			params := map[string]any{
				"pageNo":   1,
				"pageSize": s2.PageSize,
			}
			if s2.PageSize <= 0 {
				params["pageSize"] = 5
			}
			if entName != "" {
				params[s2.ParamKey] = entName
			}
			if companyID != "" {
				params["companyId"] = companyID
			}
			if s2.ExtraParams != nil {
				for k, v := range s2.ExtraParams(entName, companyID) {
					params[k] = v
				}
			}

			t0 := time.Now()
			raw := s.callQuery(ctx, s2.Pcode, params)
			elapsed := time.Since(t0).Milliseconds()

			item := map[string]any{
				"key":        s2.Key,
				"pcode":      s2.Pcode,
				"title":      s2.Title,
				"elapsed_ms": elapsed,
				"param_key":  s2.ParamKey,
			}

			// 解析响应（容忍非 JSON / 错误体）
			var parsed map[string]any
			if jerr := json.Unmarshal([]byte(raw), &parsed); jerr == nil {
				if errStr, ok := parsed["error"].(string); ok && errStr != "" {
					item["status"] = "error"
					// message 可能缺失，兜底回退到 error 字段本身，避免输出 null
					errMsg, _ := parsed["message"].(string)
					if errMsg == "" {
						errMsg = errStr
					}
					item["error"] = errMsg
					if code, ok := parsed["code"]; ok {
						item["code"] = code
					}
					if errStr == "validation" || errStr == "config" {
						// 本地校验/配置失败未触达网关，不产生计费
						item["billable"] = false
					}
				} else {
					item["status"] = "ok"
					// 生成结构化 digest（每个 section 都有）
					dgst := generateDigest(s2.Key, parsed)
					item["digest"] = dgst
					if !digestOnly {
						item["data"] = parsed
					}
				}
				// 计费口径逐节判定：仅"实际触达网关且未获豁免"才算计费单位。
				// 豁免三类：本地校验/配置失败（未触网关）、缓存命中（_cache_hit）、
				// 网关明示免计费（charge=false，code=110 无记录即此类；错误响应的
				// charge 藏在 payload 内层）。charge 字段缺失时默认计费——
				// status=ok/error 都意味着触达了网关，无免计费证据即按计费保守处理。
				if b, exists := item["billable"]; exists && b == false {
					// 本地失败，未触网关
				} else if cHit, _ := parsed["_cache_hit"].(bool); cHit {
					item["cached"] = true
				} else {
					ch, chOK := parsed["charge"].(bool)
					if !chOK {
						if pl, ok := parsed["payload"].(map[string]any); ok {
							ch, chOK = pl["charge"].(bool)
						}
					}
					if chOK && !ch {
						item["free"] = true
					} else {
						item["charged"] = true
					}
				}
			} else {
				item["status"] = "error"
				item["error"] = "响应解析失败"
				item["raw"] = truncateForLog(raw, 300)
			}
			results[idx] = item
		}(i, sec)
	}
	wg.Wait()

	// 汇总计数。billing_units 只统计"实际触达网关且 charge=true"的节：
	// 缓存命中的节（cached）与上游 code=110 免计费的节（free）都不产生计费单位。
	// 2026-09-20 修正：旧口径把全部 ok 节计入，缓存命中时虚报（实测 26 节报 26、实际计费 16）。
	ok, failed, billing, cached, free := 0, 0, 0, 0, 0
	for _, r := range results {
		switch r["status"] {
		case "ok":
			ok++
		case "error":
			failed++
		}
		switch {
		case r["charged"] == true:
			billing++
		case r["cached"] == true:
			cached++
		case r["free"] == true:
			free++
		}
	}

	out := map[string]any{
		"ent_name":         entName,
		"company_id":       companyID,
		"sections":         results,
		"ok_count":         ok,
		"failed_count":     failed,
		"skipped_count":    skipped,
		"billing_units":    billing,
		"cached_sections":  cached,
		"free_sections":    free,
		"elapsed_ms":       time.Since(start).Milliseconds(),
		"max_concurrency":  maxConcurrency,
		"digest_only":      digestOnly,
		"_note":            "尽调快照：sections 并行调用，单节失败不影响其他节。billing_units=本次实际计费节数（网关 charge=true）；cached_sections=缓存命中（0 网关调用）；free_sections=无记录免计费（code=110）。",
	}
	// 总体风险摘要（供 Agent 快速判断是否需要深入）
	out["risk_summary"] = buildRiskSummary(results)
	return mustJSON(out)
}

// buildRiskSummary 汇总所有 section 的 digest 生成总体风险概览
func buildRiskSummary(results []map[string]any) map[string]any {
	redCount, yellowCount := 0, 0
	var redSections []string
	basicMissing := false
	for _, r := range results {
		dgst, ok := r["digest"].(sectionDigest)
		if !ok {
			continue
		}
		if !dgst.HasRecords {
			if key, _ := r["key"].(string); key == "basic" {
				basicMissing = true
			}
		}
		switch dgst.RiskLevel {
		case "red":
			redCount++
			redSections = append(redSections, dgst.Summary)
		case "yellow":
			yellowCount++
		}
	}
	level := "green"
	if redCount > 0 {
		level = "red"
	} else if yellowCount > 0 {
		level = "yellow"
	}
	out := map[string]any{
		"overall_level": level,
		"red_count":     redCount,
		"yellow_count":  yellowCount,
		"red_sections":  redSections,
	}
	// basic 无记录时明确警示，防止 Agent 把 green 误读为「企业存在且低风险」
	if basicMissing {
		out["warning"] = "basic 工商信息无记录：企业可能不存在、已注销或名称有误，请先用 resolve_entity 确认主体，勿直接采信 green 结论"
	}
	return out
}

// selectSections 解析 sections 参数与 includeJudicial，返回执行列表与提示
func selectSections(sectionsParam string, includeJudicial bool) (selected []ddSection, skipped int, validationErr map[string]any) {
	// 用户显式指定节
	if s := strings.TrimSpace(sectionsParam); s != "" {
		want := map[string]struct{}{}
		for _, k := range strings.Split(s, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			want[k] = struct{}{}
		}
		// 校验未知 key
		var unknown []string
		for k := range want {
			if _, ok := ddSectionIndex[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			validKeys := make([]string, 0, len(ddSectionIndex))
			for k := range ddSectionIndex {
				validKeys = append(validKeys, k)
			}
			return nil, 0, map[string]any{
				"error":          "validation",
				"message":        "未知 sections: " + strings.Join(unknown, ","),
				"valid_sections": validKeys,
			}
		}
		for _, sec := range dueDiligenceSections {
			if _, ok := want[sec.Key]; ok {
				selected = append(selected, sec)
			}
		}
		// 显式指定时，includeJudicial 不参与过滤
		return selected, 0, nil
	}

	// 默认全开，但受 includeJudicial 控制（Risk 类不受 includeJudicial 控制）
	for _, sec := range dueDiligenceSections {
		if !includeJudicial && sec.Judicial {
			skipped++
			continue
		}
		selected = append(selected, sec)
	}
	return selected, skipped, nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// ListCapabilities 返回 MCP 数据能力说明（不调用网关）
func (s *Services) ListCapabilities() string {
	// 公共分类样本：剔除"定制"产品线的客户定制分类（国元/大衍/德勤等 7 项曾混入，
	// Agent 会把它们当作可选公共分类检索——它们不是公共能力）
	publicCats := make([]string, 0, 32)
	catSet := map[string]struct{}{}
	for _, a := range s.Cat.LatestAPIs() {
		if strings.Contains(a.ProductLine, "定制") {
			continue
		}
		if a.Category != "" {
			catSet[a.Category] = struct{}{}
		}
	}
	for c := range catSet {
		publicCats = append(publicCats, c)
	}
	sort.Strings(publicCats)
	lines := []map[string]any{
		{"scene": "快速信用预审", "mcp_tools": []string{"resolve_entity", "enterprise_credit_score"}, "description": "先用 resolve_entity 确认主体，再用 credit_score 做低成本门卫（6次API,~1s）：≥80分快速通过，50-79标准尽调，<50深度排查"},
		{"scene": "查一家公司", "mcp_tools": []string{"enterprise_basic_profile", "enterprise_due_diligence_snapshot"}, "description": "工商照面与企业尽调/风险扫描（26维度，支持 sections 子集与并发控制）"},
		{"scene": "企业工商全景", "mcp_tools": []string{"resolve_entity", "enterprise_extended_profile"}, "description": "9维度全景画像（工商+股东+人员+标签+UBO+实控+穿透+股权出质+角标）"},
		{"scene": "供应链风控", "mcp_tools": []string{"resolve_entity", "enterprise_credit_score", "supply_chain_relations", "enterprise_due_diligence_snapshot", "control_penetration"}, "description": "供应商准入：信用评分→供应链关系→风险高则全量尽调→穿透关联方"},
		{"scene": "供应链关联排查", "mcp_tools": []string{"supply_chain_relations", "enterprise_due_diligence_snapshot"}, "description": "对高风险上下游企业逐一尽调，排查供应链传染风险"},
		{"scene": "产业链招商", "mcp_tools": []string{"search_chains", "get_chain_nodes", "search_chain_enterprises", "industry_chain_profile", "investment_readiness_score"}, "description": "产业链画像→节点筛选→企业列表→投资准备度评估"},
		{"scene": "上市公司投资", "mcp_tools": []string{"resolve_entity", "listed_company_profile", "enterprise_due_diligence_snapshot"}, "description": "上市公司画像（8维度）+ 全量尽调"},
		{"scene": "企业估值辅助", "mcp_tools": []string{"listed_company_profile", "enterprise_credit_score", "enterprise_due_diligence_snapshot"}, "description": "上市画像+信用评分+尽调，为估值提供数据支撑"},
		{"scene": "找企业", "mcp_tools": []string{"search_enterprises"}, "description": "关键词、经营范围、地区等条件检索"},
		{"scene": "上市/财务", "mcp_tools": []string{"search_keeprisk_apis", "keeprisk_query"}, "description": "上市信息、财报等细项接口（51个上市API可按pcode直调）"},
		{"scene": "批量核验", "mcp_tools": []string{"batch_query_companies", "estimate_billing"}, "description": "供应商/客户名单批量工商核验（最多100条）+ 调用前计费预估"},
		{"scene": "股权穿透", "mcp_tools": []string{"control_penetration", "actual_controller"}, "description": "control_penetration=双向（investorList 对外投资+holderList 直接股东）；actual_controller=向下（该企业控制的下游企业清单）。向上实控人/UBO 请用 enterprise_extended_profile 的 beneficial_owner 节"},
		{"scene": "司法排查", "mcp_tools": []string{"enterprise_due_diligence_snapshot"}, "description": "include_judicial=true 即全 12 司法节：court_filing,zhongben,judgment,enforced,dishonest,restraint,serious_dishonesty,court_announcement,judicial_auction,court_hearing,judicial_assist,bankruptcy。不要手工缩减子集——法院公告/严重失信/司法拍卖是高频红级来源（实测漏报案例来自缩减子集）"},
		{"scene": "调用前成本预估", "mcp_tools": []string{"estimate_billing"}, "description": "纯本地计算，0次网关调用，预估各工具的API调用次数"},
		{"scene": "任意细项", "mcp_tools": []string{"keeprisk_query"}, "description": "按 pcode 调用 236+ 数据接口"},
		{"scene": "上市公司财务深度", "mcp_tools": []string{"listed_company_financials", "enterprise_due_diligence_snapshot"}, "description": "13维度财务数据（资产负债表+年度财务+利润分配+单季报表+财务比率+分红+募资等），全面分析上市企业财务状况"},
		{"scene": "上市公司市场交易", "mcp_tools": []string{"listed_company_market", "listed_company_profile"}, "description": "15维度市场数据（股权质押+限售解禁+停牌+融资融券+龙虎榜+机构持仓等），分析市场交易行为与机构动向"},
		{"scene": "企业年报与投资", "mcp_tools": []string{"annual_report_investments"}, "description": "年报+财务年报+年报指标+对外投资V2，了解企业年度经营与对外投资布局"},
		{"scene": "知识产权全景", "mcp_tools": []string{"intellectual_property_portfolio"}, "description": "5维度知识产权（专利+软件著作权+作品著作权+域名+商标），科创企业评估与技术实力摸底"},
		{"scene": "企业关联图谱", "mcp_tools": []string{"enterprise_graph_relations", "control_penetration"}, "description": "4维度关联关系（企业图谱+股权穿透股东+股权穿透对外投资+实质图谱），排查关联方与隐性控制链"},
		{"scene": "经营合规排查", "mcp_tools": []string{"operational_compliance"}, "description": "4维度合规状态（行政许可+抽查检查+资质证书+进出口信用），供应商合规准入必备"},
		{"scene": "司法深度排查", "mcp_tools": []string{"judicial_deep_dive", "enterprise_due_diligence_snapshot"}, "description": "司法协助+破产重整列表→按记录id路由明细（冻结/失效/续冻/解冻/股东变更/破产详情）；明细接口只收记录id不能直接查企业名，无记录为绿色结论"},
		{"scene": "全量数据角标", "mcp_tools": []string{"data_badge_full", "enterprise_credit_score"}, "description": "4维角标（司法+企业+经营+知识产权），比信用评分中单一角标更全面，快速评估企业数据丰富度"},
		{"scene": "企业经营详情", "mcp_tools": []string{"enterprise_business_detail", "enterprise_due_diligence_snapshot"}, "description": "8维度经营信息（分支机构+变更+标讯+信用中国许可/处罚+知识产权出质+实际股东+注销），补充尽调经营维度"},
		{"scene": "企业深度画像", "mcp_tools": []string{"enterprise_deep_profile", "enterprise_extended_profile"}, "description": "7维度工商附加（董监高+注册地核验+坐标+曾用名+联系方式+关联信息），补充全景画像未覆盖的维度"},
		{"scene": "上市公司扩展分析", "mcp_tools": []string{"listed_company_extended", "listed_company_profile"}, "description": "10维度上市扩展（银保监处罚+高管数据+行业分析+龙虎榜+A股发行），补充上市画像行业与高管维度"},
		{"scene": "招商宝深度评估", "mcp_tools": []string{"investment_analysis_suite", "investment_readiness_score"}, "description": "7维招商分析（招聘+专利+财务健康+投资行为+股东+研发），深度评估目标企业综合实力"},
		{"scene": "企业发展轨迹", "mcp_tools": []string{"enterprise_development"}, "description": "5维度发展信息（核心团队+融资历史+竞品+业务+历程），了解科创/成长型企业成长轨迹"},
		{"scene": "投资事件分析", "mcp_tools": []string{"investment_event_profile"}, "description": "5维投资信息（投资事件+趋势+合作机构排名+投资机构详情），分析企业投资行为与合作伙伴"},
		{"scene": "企业评价报告", "mcp_tools": []string{"enterprise_evaluation_report"}, "description": "5维评价（创始团队+技术竞争力+行业饱和度/垄断/成长），招商/投资评估辅助"},
		{"scene": "年报详情查询", "mcp_tools": []string{"annual_report_detail", "annual_report_investments"}, "description": "级联查询年报7个子表（变更+股东股权+对外投资+股东出资+社保+网站+担保），自动获取最新年报ID"},
		{"scene": "产业园区搜索", "mcp_tools": []string{"industrial_park_search"}, "description": "按园区名称/地域搜索产业园区及入驻企业（园区列表+企业列表+企业分布+地域筛选），招商选址必备"},
		{"scene": "校友圈企业", "mcp_tools": []string{"school_alumni_circle"}, "description": "按大学名称查该校校友创办/任职的企业（校企域：企业+校友姓名/职位/毕业院校，含学历二次校验）；关键词检索校友/母校/高校也可命中"},
	}
	out := map[string]any{
		"active_api_count": s.Cat.LenLatest(),
		"product_lines":    s.Cat.ProductLines(),
		// 2026-09-20：样本过滤"定制"产品线——此前把客户定制分类（国元/大衍/天仪/宣城/
		// 德勤/德拓/黄山等 7 项）混进公共分类样本，Agent 会把它们当作可选公共分类去检索。
		"enterprise_categories_sample": publicCats,
		"domain_playbook":              DomainPlaybook(),
		"agent_playbook":               lines,
		"python_vs_go_parity":          PythonVsGoParity(),
		"mode":                         "http",
		// 2026-09-20：删除"Markdown 文档请用 Python stdio read_keeprisk_api_doc"——
		// 本服务不存在该工具，这句话会直接诱导 Agent 调用不存在的工具（功能性缺陷）。
		"note": "Go HTTP 版；双端工具对照见 python_vs_go_parity。接口文档级参数细节用 get_keeprisk_api（支持编码或名称）。",
	}
	if s.Cache != nil {
		out["cache_stats"] = s.Cache.Stats()
	}
	if s.Quota != nil {
		out["quota_defaults"] = quota.DefaultConfig() //nolint:gosec // config not secret
	}
	return mustJSON(out)
}
