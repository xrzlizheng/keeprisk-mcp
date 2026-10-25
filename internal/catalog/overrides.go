package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件是「目录元数据的代码级修正层」。
//
// 目录（internal/catalog/data/full_catalog.json）由上游接口文档机器生成，实测发现三类问题：
//  1. required_one_of 与实际参数不符——生成器把通用的主体名 name 套到了主体字段不同的接口上；
//  2. business_params 缺失或误导——参数提示与真实用法对不上（甚至照提示传参必失败）；
//  3. example_request 键名带空格/大小写错乱（文档复制产物）。
//
// 为什么不直接改 full_catalog.json：目录重建会覆盖手改内容；代码层修正随仓库版本演进，
// 并由 metadata_test.go 守护（防止修正丢失、防止新增矛盾）。每条修正注明证据来源；
// 上游修复后应删除对应条目，测试会提示（见 TestMetadataFixesStillNeeded）。

// metadataFixes 按 pcode（大写）应用的元数据修正
var metadataFixes = map[string]func(*APIEntry){
	// ─────────────── 校企域（证据：2026-09-16 通用模型盲测实测）───────────────

	"E_017_0091": func(e *APIEntry) { // 校企统计
		// 实测：只传 catalog required_one_of 的 name → 网关 101；
		// 只传 example 的 schoolName → 被本地校验拒（要求 name）；双传才成功。
		// 结论：真实必填是 schoolName，name 属生成器误标。
		setRequiredOneOf(e, [][]string{{"schoolName"}})
		setHint(e, "schoolName", "院校名称（必填）")
		setHint(e, "type", "类型（可选）")
	},

	"E_006_0021": func(e *APIEntry) { // 校企筛选V2
		// 实测 + 原始文档（2026-09-17 校正）：
		//   - 校友圈筛选：schoolName（学校名称，模糊搜索，空格为且/逗号为或）
		//     或 searchKey={type:"4",text:"<校名>"}（type=4 学校名/简称精确搜索）；
		//     ⚠️ 平铺 type=1+text 是"企业名称模糊搜索"，语义不符（返回全国泛匹配）；
		//   - searchKey 是**对象** {type,text}，传字符串会 500；
		//   - pageNo 分页生效（目录未列出）；page 是无效名（被静默忽略）；
		//   - 区划：regionList=[{province,city,county}] 数组生效（文档确认）；扁平 province 被静默忽略且计费。
		setHint(e, "schoolName", "学校名称（**按学校查校友圈企业的推荐参数**；模糊搜索，空格为且、逗号为或）")
		setHint(e, "searchKey", `搜索关键词（**对象**，非文档参数表所写的"字符串"）：{"type":"4","text":"<校名>"}。`+
			`type 可用空格组合：1=企业名模糊、2=校友名精确、3=法人名精确、4=学校名/简称精确。`+
			`⚠️ 传字符串会返回 500（已直连网关实测：字符串→500；{type:4}→total=2126；{type:1}→total=1；`+
			`文档的「接口请求参数」表将本参数标注为"字符串"，与同文档「请求示例」中的对象写法矛盾，以示例与实际为准）`)
		setHint(e, "type", `筛选类型（配合 searchKey.text 使用；平铺 type+text 非校友圈入口）：`+
			`1=企业名称模糊搜索、2=校友名称精确、3=法人名称精确、4=学校名称/学校简称精确；多个用空格隔开，默认 1`)
		setHint(e, "text", "关键词值（配合 searchKey.type 使用；空格为且、逗号为或）")
		setHint(e, "pageSize", "每页条数（默认 10，实测 100 可用）")
		setHint(e, "pageNo", "页码（默认 1，实测生效，可用于翻页）")
		setHint(e, "regionList", `地区筛选（**数组**，选填、支持多选）：regionList=[{"province":"安徽","city":"合肥市"}]。`+
			`province/city/county 均可逐级收窄；city 或 county 必须与 province 同传才生效`)
		setHint(e, "province", "省份（⚠️实测：单独平铺传 province 会被上游静默忽略且照常计费，须嵌套在 regionList 内）")
		setHint(e, "county", "区/县（⚠️同 province：平铺无效，须嵌套在 regionList 内）")
		setHint(e, "city", "市（⚠️须嵌套在 regionList 内且与 province 同传）")
		setHint(e, "economicRegion", "经济区划（实测未生效，建议改用 regionList）")
		// 文档列出但目录缺失的常用参数（详见 eval/extract_param_docs.py 提取结果）
		setHint(e, "labelList", "科技资质（数组，选填、支持多选）：1=高新企业、2=科技型中小企业、3=瞪羚企业、4=独角兽企业等 14 种")
		setHint(e, "roundList", "融资轮次（数组，选填、支持多选）：1=天使/种子轮、2=Pre-A 至 A+轮、3=Pre-B 至 B+轮等 13 种")
		setHint(e, "scaleList", "企业规模（数组，选填、支持多选）：1=大型、2=中型、3=小型、4=微型")
		setHint(e, "entStatusList", "企业状态（数组，选填、支持多选）：1=在业/开业/在营、2=存续、10=注销、9=吊销 等")
		setHint(e, "listedList", "上市情况（数组，选填、支持多选）：0=新三板、1=H股、2=A股、3=上交所主板 等")
		setHint(e, "fieldSort", "排序字段：1=综合评分、2=成立日期、3=注册资本")
		setHint(e, "sortOrder", "排序规则：asc 升序 / desc 降序")
		// 示例不在此覆盖：由 param_meta.json 从原始文档提供（含 searchKey 对象写法），
		// 这是权威来源（override 的手写示例会遮蔽文档示例，2026-09-17 已踩过）
	},

	"E_006_0011": func(e *APIEntry) { // 院校信息
		// 接口有**两种查询模式**（依据 PDF 文档 + 2026-09-17 实测）：
		//
		//	data1 模式（传 universityName）：**院校信息查询**——返回该院校的
		//	  name/province/city/logo/backgroundImage/**tags(标签)**/description(简介)/
		//	  universitiesType(院校类型)/enName(英文名)/topLevel(热门标签)/abbreviation(简称)。
		//	  实测：universityName=哈尔滨工业大学 → total=1、tags="双一流,985,211,C9,G7"、
		//	  universitiesType="理工"、enName="Harbin Institute of Technology"，
		//	  logo/backgroundImage 为可访问的图片地址。
		//
		//	data2 模式（传 province/city）：返回该地区**企业列表 + 各自高校标签**
		//	  （entName/province/city/logo/universityNameList）。实测：province=安徽 →
		//	  total=4174，如"讯飞医疗科技"→[南京大学,中国科学技术大学,安徽大学,合肥工业大学]。
		//
		// 注意（实测）：universityName/almaMater 只对 data1 生效；与 province 同传**不会**过滤
		// data2 的企业列表（组合查询 total 仍 4174、返回相同企业）。
		//
		// 历史误判纠正：此前把"传无效参数(entName)时的默认返回（按排名的院校列表）"误述为
		// "传企业名返回通用高校榜单"——该默认返回**不是本接口的用途**，本接口是院校信息查询。
		setHint(e, "universityName", "院校名称（**data1 模式**）：返回该院校详情——标签(tags)、简介、"+
			"院校类型、英文名、简称、logo/背景图等。实测精确命中单所（哈尔滨工业大学 → total=1）")
		setHint(e, "province", "省（**data2 模式**）：返回该省**企业列表 + 各自高校标签**(universityNameList)，"+
			"如安徽 4174 家；此模式不接受院校名过滤（院校参数与之同传无效）")
		setHint(e, "city", "市（data2 模式，配合 province 使用）")
		setHint(e, "almaMater", "母校（**置顶参数，仅 data1 模式**）：传入院校名则精确匹配后置顶返回；"+
			"不传时若 sortFlag=false 则账户默认母校置顶")
		setHint(e, "sortFlag", "排序模式（data1）：true=按院校排名返回（默认）；false=母校置顶返回")
		setHint(e, "pageNo", "页码（默认 1）")
		setHint(e, "pageSize", "每页大小（默认 10）")
		setHint(e, "entName", "⚠️**非本接口参数**（目录误列）：实测传企业名被忽略、返回默认院校列表，"+
			"勿用于企业→院校反查")
		setHint(e, "name", "⚠️**非本接口参数**（目录误列），同 entName")
	},

	// ─────────────── 其他（证据：运行时代码已按真实参数调用）───────────────

	"E_007_1251": func(e *APIEntry) { // 产业链节点统计
		// 证据：internal/mcp/phase4_tools.go chainProfileSections——真实主体参数为 chainName；
		// 原相位代码需"同时补 name"绕过校验（catalog 误标 name 的副作用）。
		setRequiredOneOf(e, [][]string{{"chainName"}})
		setHint(e, "chainName", "产业链名称（必填）")
		setHint(e, "region", `地区筛选（**对象**，非数组）：region={"province":"安徽","city":"合肥市"}。`+
			`⚠️实测：平铺的 province 参数会被上游静默忽略且照常计费，必须用 region 对象；`+
			`本接口与 E_007_1131(regionList 数组)、E_007_1351(平铺 province) 的区划约定不同`)
		setHint(e, "province", "省份（⚠️实测被忽略：本接口区划须用 region 对象，勿平铺传参）")
	},

	"E_007_1601": func(e *APIEntry) { // 抽查检查
		// 证据：internal/mcp/phase5_tools.go 以 entName 调用本接口（生产可用）。
		// 2026-09-17 交叉验证纠正：此前把 companyName 也列入可选，但实测
		// **只传 companyName 返回 101 参数错误**（PDF 请求表也无该参数），
		// 本地校验放行会引导出注定失败的调用。故收窄为 entName（+companyId 兼容）。
		setRequiredOneOf(e, [][]string{{"entName", "companyId"}})
		setHint(e, "entName", "企业名称（实测可用）")
		setHint(e, "companyName", "⚠️实测：本接口不接受 companyName（只传返回 101 参数错误），请改用 entName")
	},
}

// setHint 覆盖或追加 business_params 提示
func setHint(e *APIEntry, name, hint string) {
	for i := range e.BusinessParams {
		if e.BusinessParams[i].Name == name {
			e.BusinessParams[i].Hint = hint
			return
		}
	}
	e.BusinessParams = append(e.BusinessParams, BusinessParam{Name: name, Hint: hint})
}

// setRequiredOneOf 同步修正 RequiredOneOf 与 input_schema.required_one_of。
//
// 两者都会被对外输出（get_keeprisk_api 返回 input_schema 原文），必须一致，
// 否则模型会读到自相矛盾的元数据。元素类型保持 JSON 解码形态（[]any 嵌套），
// 与 params.requiredOneOfGroups 的消费形态对齐。
func setRequiredOneOf(e *APIEntry, groups [][]string) {
	asAny := make([]any, len(groups))
	for i, g := range groups {
		row := make([]any, len(g))
		for j, f := range g {
			row[j] = f
		}
		asAny[i] = row
	}
	e.RequiredOneOf = asAny
	if e.InputSchema != nil {
		if _, ok := e.InputSchema["required_one_of"]; ok {
			e.InputSchema["required_one_of"] = asAny
		}
	}
}

// applyMetadataFixes 应用该 pcode 的修正（无修正则原样返回）
func applyMetadataFixes(e *APIEntry) {
	if f, ok := metadataFixes[strings.ToUpper(e.Pcode)]; ok {
		f(e)
	}
}

// normalizeMetadata 规整上游生成物中的键名噪音（同一字段的多种脏写法）。
//
// 上游文档常见 ' companyname '（带空格）与 companyName 并存——同一字段两套名，
// 会误导模型传参（2026-09-16 盲测）。本函数只做「去空格 + 同名去重（保留规范写法）」，
// 并把 business_params / properties / required / required_one_of / example_request
// 六处名字同步到规范写成；不做模糊改名（脏名不在本接口参数表中则保持原样，
// 交由 metadata_test.go 继续暴露）。
func normalizeMetadata(e *APIEntry) {
	// 1) 收集候选名并择优（business_params → properties → required/required_one_of）
	//
	// ⚠️ 确定性要求：names 必须排序后再处理。properties 是 map，直接 range 会让
	// "同名择优"在质量相同时的胜者随遍历顺序变化（2026-09-17 交叉验证发现
	// 同一二进制多次运行元数据不一致）。
	var names []string
	for _, p := range e.BusinessParams {
		names = append(names, p.Name)
	}
	if props, ok := e.InputSchema["properties"].(map[string]any); ok {
		pk := make([]string, 0, len(props))
		for k := range props {
			pk = append(pk, k)
		}
		sort.Strings(pk)
		names = append(names, pk...)
	}
	for _, g := range requiredGroupsOf(e) {
		names = append(names, g...)
	}
	renames, canon := canonicalNames(names)
	canonByLower := map[string]string{}
	for _, c := range canon {
		canonByLower[strings.ToLower(c)] = c
	}
	// 2) business_params：改名 + 去重（同名保留一条）
	bp := make([]BusinessParam, 0, len(e.BusinessParams))
	seenBP := map[string]struct{}{}
	for _, p := range e.BusinessParams {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if c, ok := canonByLower[strings.ToLower(name)]; ok {
			name = c
		}
		if _, dup := seenBP[strings.ToLower(name)]; dup {
			continue
		}
		seenBP[strings.ToLower(name)] = struct{}{}
		p.Name = name
		bp = append(bp, p)
	}
	e.BusinessParams = bp
	// 3) properties：改名 + 去重
	//
	// ⚠️ 必须排序遍历：去重时"保留哪个"取决于顺序，直接 range map 会让结果随运行变化
	// （2026-09-17 交叉验证发现同一二进制多次运行元数据哈希不同）。
	if props, ok := e.InputSchema["properties"].(map[string]any); ok {
		pk := make([]string, 0, len(props))
		for k := range props {
			pk = append(pk, k)
		}
		sort.Strings(pk)
		newProps := make(map[string]any, len(props))
		for _, k := range pk {
			v := props[k]
			name := strings.TrimSpace(k)
			if name == "" {
				continue
			}
			if c, ok := canonByLower[strings.ToLower(name)]; ok {
				name = c
			}
			if _, dup := newProps[name]; dup {
				continue
			}
			newProps[name] = v
		}
		e.InputSchema["properties"] = newProps
	}
	// 4) required / required_one_of：引用同步改名
	if e.InputSchema != nil {
		if req, ok := e.InputSchema["required"].([]any); ok {
			e.InputSchema["required"] = renameList(req, canonByLower)
		}
	}
	e.RequiredOneOf = renameRequiredOneOf(e.RequiredOneOf, canonByLower)
	// 5) example_request：键名归一（含去空格）
	if len(e.ExampleRequest) > 0 {
		out := make(map[string]any, len(e.ExampleRequest))
		for k, v := range e.ExampleRequest {
			name := strings.TrimSpace(k)
			if c, ok := canonByLower[strings.ToLower(name)]; ok {
				name = c
			} else if c, ok := renames[k]; ok {
				name = c
			}
			out[name] = v
		}
		e.ExampleRequest = out
	}
}

// canonicalNames 从候选名中择优：同一字段（忽略大小写与空格）只保留一个规范写法。
// 择优顺序：无首尾空格 > 含大写（camelCase）> 更短。
func canonicalNames(names []string) (renames map[string]string, canon []string) {
	renames = map[string]string{}
	best := map[string]string{} // lower → 当前最佳
	order := []string{}
	for _, orig := range names {
		name := strings.TrimSpace(orig)
		if name == "" {
			continue
		}
		k := strings.ToLower(name)
		cur, ok := best[k]
		if !ok {
			best[k] = name
			order = append(order, k)
		} else if betterName(name, cur) {
			renames[cur] = name
			best[k] = name
		}
		if final := best[k]; orig != final {
			renames[orig] = final
		}
	}
	canon = make([]string, 0, len(order))
	for _, k := range order {
		canon = append(canon, best[k])
	}
	return renames, canon
}

// betterName 判断 cand 是否比 cur 更规范（严格确定：质量相同时按字典序，避免依赖遍历顺序）
func betterName(cand, cur string) bool {
	if c, k := strings.TrimSpace(cand) == cand, strings.TrimSpace(cur) == cur; c != k {
		return c
	}
	if c, k := hasUpper(cand), hasUpper(cur); c != k {
		return c
	}
	if len(cand) != len(cur) {
		return len(cand) < len(cur)
	}
	return cand < cur // 同质量时字典序取胜，保证确定性
}

func hasUpper(s string) bool { return strings.ToLower(s) != s }

// renameList 按规范名映射改写字符串列表（保留未知项原样）
func renameList(list []any, canonByLower map[string]string) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			if c, hit := canonByLower[strings.ToLower(strings.TrimSpace(s))]; hit {
				out = append(out, c)
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

// renameRequiredOneOf 同步 required_one_of 的字段名（含嵌套结构）
func renameRequiredOneOf(raw any, canonByLower map[string]string) any {
	switch t := raw.(type) {
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			if g, ok := item.([]any); ok {
				out = append(out, renameList(g, canonByLower))
				continue
			}
			out = append(out, renameList([]any{item}, canonByLower)[0])
		}
		return out
	case [][]string:
		out := make([]any, 0, len(t))
		for _, g := range t {
			row := make([]any, 0, len(g))
			for _, f := range g {
				if c, hit := canonByLower[strings.ToLower(strings.TrimSpace(f))]; hit {
					row = append(row, c)
					continue
				}
				row = append(row, f)
			}
			out = append(out, row)
		}
		return out
	default:
		return raw
	}
}

// requiredGroupsOf 读取条目的 required_one_of（兼容 JSON 解码的 []any 形态与手写 [][]string）。
// 仅用于本文件的关键字归一；参数校验仍以 params 包为准。
func requiredGroupsOf(e *APIEntry) [][]string {
	var groups [][]string
	switch t := e.RequiredOneOf.(type) {
	case []any:
		for _, item := range t {
			switch g := item.(type) {
			case []any:
				row := make([]string, 0, len(g))
				for _, x := range g {
					row = append(row, strings.TrimSpace(fmt.Sprint(x)))
				}
				groups = append(groups, row)
			case string:
				groups = append(groups, []string{strings.TrimSpace(g)})
			}
		}
	case [][]string:
		for _, g := range t {
			groups = append(groups, g)
		}
	case []string:
		for _, s := range t {
			groups = append(groups, []string{s})
		}
	}
	return groups
}
