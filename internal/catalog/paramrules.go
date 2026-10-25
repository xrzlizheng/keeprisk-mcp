package catalog

import (
	"fmt"
	"strings"
)

// 参数要求规则（Param Rules）：把"实测确认的参数传法要求"集中定义一次，
// 在 MCP 的四条读取路径自动浮现——
//
//	search_keeprisk_apis → param_requirements 字段
//	get_keeprisk_api     → param_requirements 字段
//	validate_keeprisk_params → errors/warnings
//	keeprisk_query       → 调用前拦截（Blocking）或调用后警告
//
// 解决的问题（2026-09-17 用户反馈）：模型调用 MCP 服务时无法从目录得知
// "参数该怎么传"，尤其是**传错会被上游静默忽略**的情形——返回的是全国数据
// 却看起来像筛选成功，且照常计费（最危险的一类）。
//
// 维护约定：
//   - 每条规则必须有**线上实测证据**（写入注释），不确定的不要加；
//   - Blocking=true 仅用于"参数被静默忽略且本接口内存在正确写法"的情形
//     （阻止调用＝避免错误数据＋避免无效计费）；
//   - 接口修复后（上游统一参数格式）应删除对应规则，测试会提示。

// ParamRuleLevel 规则类型
type ParamRuleLevel string

const (
	ParamRequired ParamRuleLevel = "required" // 实测必填（目录未声明或声明错误）
	ParamFormat   ParamRuleLevel = "format"   // 传法要求（容器格式）
	ParamIgnored  ParamRuleLevel = "ignored"  // 实测被上游静默忽略
)

// ParamRule 单条参数要求
type ParamRule struct {
	Param string         // 参数名（组合参数可用 "a/b" 写法）
	Level ParamRuleLevel // 规则类型
	Note  string         // 说明（为什么会出问题）
	Fix   string         // 正确写法（可照抄）
	// Blocking 为 true 时，命中 Bad 判定将阻止调用（返回 validation 错误）
	Blocking bool
	// Bad 判定"该参数的用法有问题"；nil 表示仅作提示、不参与校验
	Bad func(params map[string]any) bool
}

// paramRules 按 pcode（大写）登记参数要求规则
var paramRules = map[string][]ParamRule{
	// ─────────── 校企筛选V2（证据：2026-09-17 原始文档 + 实测校正）───────────
	//
	// ⚠️ 2026-09-17 更正：此前基于"试探式实测"得出的两条结论均被原始文档推翻——
	//   - "searchKey 返回 500、不可用" ❌ 实际 searchKey 是**对象** {type,text}，
	//     文档示例 {"searchKey":{"type":"1 2 3 4","text":"哈工大"}}；传字符串才 500。
	//   - "校友圈筛选用 type=1+text" ❌ type=1 是**企业名称模糊搜索**（返回全国泛匹配
	//     total=71990）；文档 type 取值 1=企业名/2=校友名/3=法人名/**4=学校名或简称**，
	//     查校友圈应用 schoolName 或 searchKey.type=4（实测 total=2126，与校企统计一致）。
	//
	// 教训：参数结论必须**先读原始文档**（markdown/.../[pcode]标题.md），
	// 试探式实测只能作为补充验证，不能作为唯一依据。
	"E_006_0021": {
		{
			Param: "province/city/county", Level: ParamIgnored, Blocking: true,
			Note: "平铺传区划会被上游静默忽略且照常计费（返回的是全国数据，看起来却像筛选成功）；" +
				"文档：regionList 为**数组** [{province,city,county}]，支持多选",
			Fix: `"regionList":[{"province":"安徽","city":"合肥市"}]`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "regionList") {
					return false // 已用正确写法
				}
				return hasAnyParam(p, "province", "city", "county")
			},
		},
		{
			Param: "schoolName", Level: ParamFormat,
			// 文档原文为"选填"，故 level 用 format（用法说明）而非 required（必填）——
			// 2026-09-17 交叉验证指出：标 required 会让模型误以为必须传，而本接口还有
			// companyId、searchKey.type=2/4 等合法路径。
			Note: "按学校查校友圈企业的**推荐参数**（文档：学校名称，选填，模糊搜索，空格为且、逗号为或）；" +
				"等价写法 searchKey={type:\"4\",text:\"<校名>\"}（type=4 为学校名称/简称精确搜索）",
			Fix: `"schoolName":"中国科学技术大学"`,
			Bad: func(p map[string]any) bool {
				// 既未传 schoolName 也未用 searchKey/companyId（其他合法路径）→ 提示推荐参数
				if hasAnyParam(p, "schoolName") || hasAnyParam(p, "searchKey") || hasAnyParam(p, "companyId") {
					return false
				}
				return paramStr(p, "text") != ""
			},
		},
		{
			Param: "searchKey", Level: ParamFormat, Blocking: true,
			Note: `searchKey 是**对象** {"type":"1 2 3 4","text":"关键词"}，传字符串会返回 500；` +
				`type 可用空格组合多个（1=企业名模糊 2=校友名精确 3=法人名精确 4=学校名/简称精确）`,
			Fix: `"searchKey":{"type":"4","text":"中国科学技术大学"}`,
			Bad: func(p map[string]any) bool {
				v, ok := p["searchKey"]
				if !ok {
					return false
				}
				_, isStr := v.(string)
				return isStr // 字符串形态一定失败
			},
		},
		{
			Param: "type", Level: ParamIgnored,
			Note: "平铺的 type+text 不是校友圈筛选入口：type=1 表示**企业名称模糊搜索**" +
				"（实测传校名返回全国泛匹配 total=71990，语义不符）；按学校筛选请用 schoolName",
			Fix: "",
			Bad: func(p map[string]any) bool {
				// 平铺 type（非 searchKey 内）且未用 schoolName → 提示可能语义不符
				if _, hasSchool := p["schoolName"]; hasSchool {
					return false
				}
				if _, hasSK := p["searchKey"]; hasSK {
					return false
				}
				return paramStr(p, "text") != ""
			},
		},
	},

	// ─────────── 产业链节点统计（证据：2026-09-17 实测，修复静默失效 bug）───────────
	"E_007_1251": {
		{
			Param: "province/city", Level: ParamIgnored, Blocking: true,
			Note: "本接口区划须用 region **对象**；平铺 province/city 会被静默忽略且照常计费" +
				"（实测：无区划节点总数 396 万，region={安徽} 降至 21 万，而平铺 province 与无区划完全相同）",
			Fix: `"region":{"province":"安徽","city":"合肥市"}`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "region") {
					return false
				}
				return hasAnyParam(p, "province", "city")
			},
		},
	},

	// ─────────── 产业链企业筛选（证据：2026-09-17 实测）───────────
	"E_007_1131": {
		{
			Param: "province/city/county", Level: ParamIgnored, Blocking: true,
			Note: "本接口区划须用 regionList **数组**；平铺 province 会被静默忽略且照常计费" +
				"（实测：平铺安徽返回沪陕甘京等全国企业，regionList 安徽返回 10/10 安徽）",
			Fix: `"regionList":[{"province":"安徽","city":"合肥市"}]`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "regionList") {
					return false
				}
				return hasAnyParam(p, "province", "city", "county")
			},
		},
	},

	// ─────────── 产业链地区统计 / 产业链财务数据（证据：2026-09-17 实测）───────────
	// 这两个接口与校企筛选**相反**：扁平 province 生效、regionList 无效/报错。
	"E_007_1351": {
		{
			Param: "regionList", Level: ParamIgnored, Blocking: true,
			Note: "本接口区划用**扁平** province/city/county；regionList 会被忽略" +
				"（实测：扁平安徽 12198 家 vs 全国 407954 家，regionList 与无区划完全相同）",
			Fix:  `"province":"安徽"`,
			Bad:  func(p map[string]any) bool { return hasAnyParam(p, "regionList") },
		},
	},
	// ─────────── 产业链财务数据（证据：2026-09-17 PDF 文档 + 交叉验证纠正）───────────
	//
	// ⚠️ 规则更正：此前登记"regionList 会触发 101"是**基于不存在参数的臆测**——
	// 该接口文档根本没有 regionList 参数（核查 PDF：参数仅 chainHis/areaLevel/province 必填
	// + city/county/economicRegion 选填）。实测传 regionList 返回 code=100（被静默忽略），
	// 不是 101。且原 Fix 示例漏了**必填的 areaLevel**，照抄会失败（真 101 来自缺必填项）。
	"E_007_1171": {
		{
			Param: "areaLevel", Level: ParamRequired, Blocking: true,
			Note: "本接口**必填** areaLevel（区域等级）+ chainHis（产业链名称）+ province（省/全国）；" +
				"文档取值：0=全国、1=省、2=市、3=区县、4=经济区划",
			Fix: `"chainHis":"新能源汽车产业链","areaLevel":"1","province":"安徽"`,
			Bad: func(p map[string]any) bool {
				// 传了 chainHis（说明在查本接口）但缺 areaLevel → 会被网关拒 101
				if paramStr(p, "chainHis") == "" {
					return false
				}
				return anyMissing(p, "areaLevel", "province")
			},
		},
		{
			Param: "regionList", Level: ParamIgnored, Blocking: true,
			Note: "本接口**无 regionList 参数**（文档未列出），传入会被静默忽略；" +
				"区划请用 areaLevel + province/city/county/economicRegion",
			Fix: `"areaLevel":"1","province":"安徽","city":"合肥市"`,
			Bad: func(p map[string]any) bool { return hasAnyParam(p, "regionList") },
		},
	},

	// ─────────── 产业链数据分析（证据：2026-09-17 交叉验证纠正）───────────
	//
	// ⚠️ 规则更正：此前登记"区划不生效"是**错误结论**——只试了扁平 province（被忽略、与基线一致）
	// 就断言无区划能力。交叉验证实测：region={"province":"安徽"} 使响应真实变化（摘要 66f205→92701d），
	// 即本接口区划须用 **region 对象**（与 E_007_1251 同类）。
	"E_017_0081": {
		{
			Param: "province/city/county", Level: ParamIgnored, Blocking: true,
			Note: "本接口区划须用 **region 对象**；平铺 province/city 会被静默忽略且照常计费" +
				"（实测：region={province:安徽} 响应变化，扁平 province 与基线完全相同）",
			Fix: `"region":{"province":"安徽"}`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "region") {
					return false
				}
				return hasAnyParam(p, "province", "city", "county")
			},
		},
	},

	// ─────────── 院校信息（证据：2026-09-17 PDF 文档 + 实测）───────────
	// 双模式：universityName→院校详情(data1)；province/city→该省企业+高校标签(data2)。
	// 目录误列 entName/name，传入会被忽略并返回默认院校列表（看起来像结果、实为无关数据）且照常计费。
	"E_006_0011": {
		{
			Param: "entName/name", Level: ParamIgnored, Blocking: true,
			Note: "本接口按院校名(universityName)或省份(province)查询，**不接受企业名**；" +
				"传企业名会被忽略并返回默认院校列表（按排名的高校，看起来像结果但与本意无关）且照常计费",
			Fix: `查院校信息：{"universityName":"哈尔滨工业大学"}；查某省企业的高校标签：{"province":"安徽"}`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "universityName") || hasAnyParam(p, "province") {
					return false // 已用正确参数
				}
				return hasAnyParam(p, "entName", "name")
			},
		},
	},

	// ─────────── 查电话 / 查企业（证据：2026-09-17 实测，属"混合声明"接口）───────────
	// 目录同时声明 region 对象与扁平 province，模型必然传错其一：实测 region 对象生效、扁平被忽略。
	"E_007_0731": {regionObjectRule("查电话")},
	"E_007_0861": {regionObjectRule("查企业")},

	// ─────────── 企业筛选V4（证据：2026-09-17 实测）───────────
	// region 数组的内层键是 pr/ci/co 简写，不是 province/city/county。
	"E_007_1331": {
		{
			Param: "region/province", Level: ParamFormat, Blocking: true,
			Note: `本接口的 region 数组内层键是 **简写 pr/ci/co**，不是 province/city/county；` +
				`平铺 province 同样会被静默忽略（实测：[{pr:"安徽"}] 生效，[{province:"安徽"}] 与扁平均被忽略）`,
			Fix: `"region":[{"pr":"安徽","ci":"合肥市"}]`,
			Bad: func(p map[string]any) bool {
				if hasAnyParam(p, "province", "city", "county") {
					return true // 扁平一定无效
				}
				arr, ok := p["region"].([]any)
				if !ok {
					return false // 未传 region：不拦（由既有逻辑处理）
				}
				for _, it := range arr {
					m, _ := it.(map[string]any)
					if m == nil {
						continue
					}
					if _, bad := m["province"]; bad {
						return true // 用了长键名而非 pr
					}
					if _, bad := m["city"]; bad {
						return true
					}
				}
				return false
			},
		},
	},

	// ─────────── 园区列表（证据：2026-09-17 实测：扁平生效、regionList 被忽略）───────────
	"E_007_0971": {
		{
			Param: "regionList", Level: ParamIgnored, Blocking: true,
			Note:  "本接口区划用**扁平** province/city/county；regionList 会被忽略（实测与基线响应完全相同）",
			Fix:   `"province":"安徽","city":"合肥市"`,
			Bad:   func(p map[string]any) bool { return hasAnyParam(p, "regionList") },
		},
	},

	// ─────────── 财务年报两接口的 year 期次（证据：2026-09-18 六主体实测）───────────
	// 省略 year 时上游返回**固定默认期**（实测恒 2023 期，茅台/比亚迪显式查 2024 也 110），
	// 且响应体不含任何期次字段——调用方无从判断数据属于哪一年，极易误读为"最新年报"。
	"E_007_0931": {
		{
			Param: "year", Level: ParamFormat,
			Note: "未传 year 时上游返回固定默认期（实测恒 2023 期），**不是最新报告期**；且响应不含期次字段。" +
				"聚合工具 annual_report_investments 会回告 financial_year，直调时请自行记录所传 year",
			Fix: `"year":"2023"`,
			Bad: func(p map[string]any) bool {
				return hasAnyParam(p, "entName", "creditNo") && paramStr(p, "year") == ""
			},
		},
	},
	"E_007_0941": {
		{
			Param: "year", Level: ParamFormat,
			Note: "未传 year 时上游返回固定默认期（实测恒 2023 期），**不是最新报告期**；且响应不含期次字段（15 个指标无一标注所属年度）",
			Fix: `"year":"2023"`,
			Bad: func(p map[string]any) bool {
				return hasAnyParam(p, "entName", "creditNo") && paramStr(p, "year") == ""
			},
		},
	},

	// ─────────── 招聘信息分析（证据：2026-09-20 实测：只传 companyName 恒 101）───────────
	"E_017_0131": {
		{
			Param: "type", Level: ParamRequired, Blocking: true,
			Note:  "type 为实测必填（目录 required_one_of 漏标）：只传 companyName 恒返回 101 参数错误；{companyName, type:1} 实测成功",
			Fix:   `"type": 1`,
			// 用键存在性判断而非 paramStr：聚合层注入的 type 是 int(1)，
			// paramStr 只认字符串会把已传 type 误判为缺失（2026-09-20 实测误拦 recruitment_analysis）
			Bad: func(p map[string]any) bool {
				if _, ok := p["type"]; ok {
					return false
				}
				return hasAnyParam(p, "companyName", "companyId")
			},
		},
	},

	// ─────────── 政策详情V2 的 id 来源（证据：2026-09-22 apitest 实测）───────────
	// 级联接口：id 即**政策列表**返回的 uid——同一个 32 位主键值，列表项字段名为 uid、
	// 本接口参数名为 id（实测同值往返返回同一条政策：标题/发布日期完全一致）。
	// 另一合法来源：详情响应 dataList（推荐列表）的 id，可继续级联。
	// 自行构造的 id 实测 code=110 无数据（不计费），但"无数据"易被误读为"政策不存在"；
	// 参数名误写 uid/policyId 实测 code=101（不计费）。
	// 非阻断：格式仅为启发式（不信形态硬拦合法 id）；提示级联来源即可。
	"E_007_1451": {
		{
			Param: "id", Level: ParamFormat,
			Note: "id 就是政策列表返回的 **uid**（同一个 32 位主键，列表里叫 uid、这里叫 id，不是两个东西）；" +
				"先调政策列表检索目标政策取其 uid，或用详情响应 dataList 的 id 继续级联。" +
				"自行构造的 id 返回 110 无数据（实测不计费），不是「政策不存在」",
			Fix: `先调政策列表（keeprisk_query pcode=政策列表）→ 取 list[].uid → 传 "id":"<uid>"`,
			Bad: func(p map[string]any) bool {
				v, ok := p["id"]
				if !ok || v == nil {
					return false // 缺失由 requiredFields 拦截，职责不重叠
				}
				// 数字等非字符串形态：网关按字符串收（SerializeBody 转换），但
				// paramStr 对非 type 键的数字返回空会漏掉提示——统一按可疑处理
				id, isStr := v.(string)
				if !isStr {
					return true
				}
				return !isPolicyUID(strings.TrimSpace(id))
			},
		},
	},
}

// isPolicyUID 政策主键形态：32 位十六进制（实测样本为小写；大小写均接受，宽容处理）。
// 仅用于「疑似自行构造的 id」提示，不做强校验——形态不符时给出级联来源指引，
// 避免模型把 110 无数据误读为政策不存在。
func isPolicyUID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// ParamRulesForTest 暴露规则表给测试做一致性校验（生产代码不应依赖）
func ParamRulesForTest() map[string][]ParamRule {
	return paramRules
}

// SubjectFieldsForTest 暴露主体字段推导给测试
func SubjectFieldsForTest(entry *APIEntry) []string {
	return subjectFieldsIn(entry)
}
// 适用：混合声明接口（目录同时列出 region 与扁平 province），实测仅 region 生效。
func regionObjectRule(title string) ParamRule {
	return ParamRule{
		Param: "province/city/county", Level: ParamIgnored, Blocking: true,
		Note: title + "的区划须用 region **对象**；平铺 province/city 会被静默忽略且照常计费" +
			"（实测：region={province} 响应变化，扁平 province 与无区划完全相同）",
		Fix: `"region":{"province":"北京","city":"北京市"}`,
		Bad: func(p map[string]any) bool {
			if hasAnyParam(p, "region") {
				return false
			}
			return hasAnyParam(p, "province", "city", "county")
		},
	}
}

// ParamRequirements 返回该接口的参数要求（供 search/get 输出；无规则返回 nil）
func ParamRequirements(pcode string) []map[string]any {
	rules := paramRules[strings.ToUpper(strings.TrimSpace(pcode))]
	if len(rules) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		item := map[string]any{
			"param": r.Param,
			"level": string(r.Level),
			"note":  r.Note,
		}
		if r.Fix != "" {
			item["fix"] = r.Fix
		}
		out = append(out, item)
	}
	return out
}

// CheckParamRules 按规则校验入参，返回 errors（阻断）与 warnings（提示）。
// 供 validate_keeprisk_params 与 keeprisk_query 共用。
func CheckParamRules(pcode string, params map[string]any) (errs, warns []string) {
	rules := paramRules[strings.ToUpper(strings.TrimSpace(pcode))]
	if len(rules) == 0 || params == nil {
		return nil, nil
	}
	for _, r := range rules {
		if r.Bad == nil || !r.Bad(params) {
			continue
		}
		msg := fmt.Sprintf("参数 %s：%s", r.Param, r.Note)
		if r.Fix != "" {
			msg += "；正确写法：" + r.Fix
		}
		if r.Blocking {
			errs = append(errs, msg)
		} else {
			warns = append(warns, msg)
		}
	}
	return errs, warns
}

// hasAnyParam 判断参数中是否存在任一键（空值不算）
func hasAnyParam(params map[string]any, keys ...string) bool {
	for _, k := range keys {
		if paramStr(params, k) != "" {
			return true
		}
	}
	if _, ok := params["regionList"]; ok && containsKey(keys, "regionList") {
		return true
	}
	if v, ok := params["region"]; ok && containsKey(keys, "region") {
		switch t := v.(type) {
		case map[string]any:
			return len(t) > 0
		case []any:
			return len(t) > 0
		}
	}
	return false
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// anyMissing 判断 key 中是否有缺失（空值）的（用于"必填但目录未声明"的校验）
func anyMissing(params map[string]any, keys ...string) bool {
	for _, k := range keys {
		if paramStr(params, k) == "" {
			return true
		}
	}
	return false
}

// paramStr 取字符串参数（非字符串/空返回空串）
func paramStr(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	if f, ok := v.(float64); ok && key == "type" {
		// JSON 解码可能把 "1" 变成 1（数字）——归一为字符串比较
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%v", f), ".0"), ".00")
	}
	return ""
}

// subjectFieldsIn 返回该接口声明的"主体字段"（按业务优先级排序）。
//
// 背景（2026-09-17 全量审计）：236 个接口的主体字段约定有 6 种（entName/companyName/name/
// text/keyword/chainHis 等），分布为 name 81、entName 19、companyName 15……
// 逐个接口硬编码规则不可维护，故改为**从目录按接口自动推导**并提示模型，
// 与 params 包既有的别名归一 + mirrorSubjectFields 形成互补。
func subjectFieldsIn(entry *APIEntry) []string {
	if entry == nil {
		return nil
	}
	// 主体字段全集（按业务语义排序：企业名 → 通用名 → 其他）
	order := []string{"entName", "companyName", "name", "schoolName", "universityName",
		"chainName", "chainHis", "keyword", "text", "personName", "companyId", "creditCode"}
	declared := map[string]bool{}
	for _, p := range entry.BusinessParams {
		declared[strings.TrimSpace(p.Name)] = true
	}
	if props, ok := entry.InputSchema["properties"].(map[string]any); ok {
		for k := range props {
			declared[strings.TrimSpace(k)] = true
		}
	}
	var out []string
	for _, f := range order {
		if declared[f] {
			out = append(out, f)
		}
	}
	return out
}

// SubjectRequirementNote 返回"该接口的主体字段要求"提示（供 validate/query 警告与
// get_keeprisk_api 输出）。仅当接口声明了主体字段时返回。
//
// 目的：模型传参前即可知道"这个接口要的是 entName 还是 name"，
// 避免把 entName 传给只认 name 的接口（或反之）导致 101/空结果。
func SubjectRequirementNote(entry *APIEntry) string {
	fields := subjectFieldsIn(entry)
	if len(fields) == 0 {
		return ""
	}
	// 只保留企业/人员主体类字段（排除 id 类，避免噪音）
	var main []string
	for _, f := range fields {
		switch f {
		case "companyId", "creditCode":
			continue
		}
		main = append(main, f)
	}
	if len(main) == 0 {
		return ""
	}
	return fmt.Sprintf("本接口主体字段为 %s（部分接口可用别名自动映射，但建议直接使用声明字段以免歧义）",
		strings.Join(main, " / "))
}

// SubjectMismatchWarning 检查入参主体字段是否与接口声明匹配，返回提示。
//
// 只在"传了主体字段但都不是本接口声明的"时提示（避免误报），
// 例：向只认 name 的接口传了 entName → 提示应改用 name。
func SubjectMismatchWarning(entry *APIEntry, params map[string]any) string {
	if entry == nil || len(params) == 0 {
		return ""
	}
	fields := subjectFieldsIn(entry)
	if len(fields) == 0 {
		return ""
	}
	declared := map[string]bool{}
	for _, f := range fields {
		declared[f] = true
	}
	// 入参中出现的主体字段（来自已知主体字段全集）
	all := []string{"entName", "companyName", "name", "schoolName", "universityName",
		"chainName", "chainHis", "keyword", "text", "personName"}
	var used, wrong []string
	for _, f := range all {
		if paramStr(params, f) == "" {
			continue
		}
		used = append(used, f)
		if !declared[f] {
			wrong = append(wrong, f)
		}
	}
	if len(used) == 0 || len(wrong) == 0 {
		return ""
	}
	// 有任一匹配字段就不提示（多字段接口常见）
	for _, f := range used {
		if declared[f] {
			return ""
		}
	}
	return fmt.Sprintf("入参用的主体字段 %s 不在本接口声明中；本接口主体字段为 %s。字段不匹配可能导致 101 或返回空结果",
		strings.Join(wrong, " / "), strings.Join(fields, " / "))
}
