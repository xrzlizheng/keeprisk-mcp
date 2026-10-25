package params

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

var aliases = map[string]string{
	"ent_name":           "entName",
	"company_id":         "companyId",
	"company_name":       "companyName", // E_011_0391/E_011_0421 等接口主体字段为 companyName
	"page_no":            "pageNo",
	"page_size":          "pageSize",
	"id_no":              "idNo",
	"credit_code":        "creditCode",
	"reg_no":             "regNo",
	"legal_person_names": "legalPersonNames",
	"business_scope":     "businessScope",
	"reg_addr":           "regAddr",
}

var skipUnknown = map[string]struct{}{
	"bindingType": {}, "sort": {}, "sortOrder": {}, "region": {}, "industry": {},
	// 高级筛选工具构建的复合参数（非网关直接字段）
	"regionList": {}, "excludeRegionList": {}, "industryList": {}, "companyState": {},
	"companyIdList": {}, "entNames": {}, "searchKey": {}, "point": {},
}

// requiredFields 按 PDF 文档确认的必填字段（覆盖 catalog required_one_of 不准确的情况）
// 主体字段名因接口而异：entName/name/text/companyName/keyword/chainHis
var requiredFields = map[string][]string{
	"E_003_0311": {"name"},    // 终本案件
	"E_003_0351": {"entName"}, // 立案信息
	"E_003_0402": {"text"},    // 裁判文书（PDF 明确 text 必填）
	"E_003_0702": {"name"},    // 被执行人V2（PDF 允许 name/idNo 二选一，默认 name）
	"E_003_0802": {"name"},    // 失信人V2（同上）
	"E_003_1811": {"name"},    // 破产重整查询（主体字段为 name）
	"E_003_1501": {},          // 司法拍卖（entName/name/courtName/sTime/eTime 必填其一，由 required_one_of 处理）
	"P_003_0361": {"entName"}, // 限制高消费（PDF 明确 entName 必填）
	"E_003_0411": {"entName"}, // 信用中国_严重失信（PDF 明确 entName 必填，catalog 误标为 name）
	"E_007_0201": {"entName"}, // 实际控制权
	"E_007_0501": {},          // 工商基本信息（entName/companyId 二选一，由 required_one_of 处理）
	"E_007_0601": {"entName"}, // 主要人员
	"E_007_0621": {"entName"}, // 股权穿透（PDF 入参为 entName，catalog 误标为 name）
	"E_007_0901": {"entName"}, // 工商股东
	"E_007_1001": {"entName"}, // 行政处罚
	"E_007_1201": {"entName"}, // 经营异常
	"E_007_1401": {"entName"}, // 严重违法
	"E_007_1501": {"entName"}, // 清算信息
	"E_007_2101": {"entName"}, // 股权出质
	"E_007_1701": {"entName"}, // 动产抵押信息
	"E_007_2301": {"entName"}, // 司法协助
	"E_008_0001": {"entName"}, // 欠税公告
	"E_008_0041": {"name"},    // 税务违法（主体字段为 name）
	"E_003_0601": {"entName"}, // 法院公告
	"E_003_0901": {"entName"}, // 开庭公告
	"E_011_0391": {},          // 供应商与客户（companyName/companyId 二选一）
	"E_011_0401": {"name"},    // 税务评级（主体字段为 name）
	"E_011_0421": {},          // 产品质量抽查（companyName/companyId 二选一）
	"E_011_0801": {"name"},    // 进出口信用（主体字段为 name）
	"E_009_0101": {"keyword"}, // 舆情检索（主体字段为 keyword）
	// E_011_0251 企业财务状况评分：文档必填为 key（企业名称/统一社会信用代码）+ year（年份）。
	// 2026-09-17 交叉验证纠正：此前硬编码 entName 会把**文档示例用法**（{"key","year"}）
	// 直接拦在本地，模型照文档调用反而失败。实测网关对 entName 也接受（code=110 无数据，非 101），
	// 故主体字段改为"key 或 entName 二选一"（在 requiredOneOfOverrides 中表达），year 仍必填。
	"E_011_0251": {"year"},
	"E_011_0361": {"entName"},   // 企业经营评分
	"E_017_0111": {"companyName"}, // 企业科创评分
	"E_017_0121": {"companyName"}, // 企业综合评分
	"E_007_1391": {},            // 数据角标V2（companyName/companyId 二选一）
	"E_007_1021": {"entName"},   // 企业标签
	// 2026-09-18 修正：8 个接口的必填主体字段实为 companyName（文档原文 companyname
	// 为笔误，网关实测只认 companyName；传 name 会被拒"未知参数 name"）。
	// 旧值硬编码 {"name"}，导致**按文档传 companyName+type 的合法调用被本地校验
	// 以"缺少必填字段: name"拦截**，从未到达网关（实测 E_010_0401 即如此）。
	"E_010_0401": {"companyName"}, // 上市公司概况
	"E_010_0241": {"companyName"}, // 上市公司关键指标数据
	"E_010_0501": {"companyName"}, // 上市公司股票行情
	"E_010_0601": {"companyName"}, // 上市公司十大股东
	"E_010_0541": {"companyName"}, // 上市公司十大流通股东
	"E_010_0321": {"companyName"}, // 上市公司股本结构
	"E_010_0571": {"companyName"}, // 上市公司主营业务
	// 注意：定量评级 E_010_1201 的主体字段**确实是 name**（文档「name 企业名称 必填」、
	// 目录 business_params=[name,type]、聚合器 ParamKey=name 三方一致），
	// 不在上述 companyName 修正范围内，勿随批量修改误伤（2026-09-18 实测回归）。
	"E_010_1201": {"name"}, // 定量评级
	"E_005_0091": {},         // 企业产业链图谱（companyName/entName 均可）
	"E_007_1251": {},          // 产业链节点统计（chainName/name 均可，由 chain profile 并行传入）
	"E_017_0081": {},          // 产业链数据分析（主体字段为 chainName）
	"E_007_1351": {},          // 产业链地区统计（主体字段为 chainName）
	"E_007_1171": {},          // 产业链财务数据（主体字段为 chainHis）
	"E_017_0161": {"companyName"}, // 投资意愿评分
	"E_017_0231": {"companyName"}, // 投资意愿分析
	"E_017_0201": {"entName"}, // 企业关联信息查询（PDF 入参为 entName，catalog 误标为 name）

	// ─── Phase 5 聚合工具 pcode ───
	// 上市公司财务数据（13 API，主体 companyName）
	"E_010_0211": {}, // 资产负债表
	"E_010_0221": {}, // 年度财务数据
	"E_010_0231": {}, // 利润分配表
	"E_010_0251": {}, // 单季现金流量表
	"E_010_0331": {}, // 单季利润表
	"E_010_0341": {}, // 单季度财务数据
	"E_010_0431": {}, // 报告期财务数据
	"E_010_0581": {}, // 现金流量表
	"E_010_0901": {}, // 财务比率数据
	"E_010_0181": {}, // 分红转增
	"E_010_0191": {}, // 分红配股
	"E_010_0561": {}, // 上市以来募资统计
	"E_010_0551": {}, // 增发数据
	// 上市公司市场数据（17 API，主体 name）
	"E_010_0411": {}, // 股权质押明细
	"E_010_0421": {}, // 限售解禁
	"E_010_0441": {}, // 停牌历史
	"E_010_0701": {}, // 停牌实时数据
	"E_010_0271": {}, // 股东户数
	"E_010_0281": {}, // 机构持股
	"E_010_0461": {}, // 股东人数与股价比较
	"E_010_0471": {}, // 融资买入
	"E_010_0481": {}, // 融券卖出
	"E_010_1001": {}, // 融资融券余额
	"E_010_0531": {}, // 大宗交易
	"E_010_0351": {}, // 龙虎榜明细
	"E_010_0451": {}, // 龙虎榜席位统计
	"E_010_0511": {}, // 机构席位统计
	"E_010_0521": {}, // 机构席位交易明细
	"E_010_0001": {}, // 全部A股上市公司
	"E_010_1301": {}, // A股退市上市公司
	// 企业年报与投资（4 API）
	"E_007_2901": {},          // 企业年报（entName）
	"E_007_0931": {},          // 企业财务年报（entName）
	"E_007_0941": {},          // 企业财务年报指标（entName）
	"E_017_0191": {"companyName"}, // 对外投资V2（companyName）
	// 知识产权组合（5 API，主体 name）
	"E_012_0001": {}, // 专利
	"E_012_0101": {}, // 软件著作权
	"E_012_0201": {}, // 作品著作权
	"E_012_0301": {}, // 网址域名备案
	"E_012_0401": {}, // 商标
	// 企业关联图谱（3 API，主体 entName）
	"E_007_0401": {}, // 企业图谱
	"E_007_0631": {}, // 股权穿透-节点
	"E_007_5101": {}, // 实质图谱
	// 经营合规（4 API）
	"E_007_1101": {},          // 行政许可（entName）
	"E_007_1601": {},          // 抽查检查（entName）
	"E_011_0202": {"name"},    // 资质证书（name）
	// E_011_0801 已存在（进出口信用）
	// 司法/破产详情（6 API）：主体为【列表记录 id】，不是企业名。
	// 链路：司法协助列表(E_007_2301,entName) → 记录 id + typeState → 2401~2801；
	//       破产重整查询(E_003_1811,name) → 案件 id → 1821。
	// 企业名误作 id 曾致网关 101 参数错误 6 节全灭（2026-09-17 线上实测），本地拦截。
	"E_003_1821": {"id"}, // 破产重整案件详情（id 来自 E_003_1811）
	"E_007_2401": {"id"}, // 司法冻结明细（id 来自 E_007_2301，typeState=冻结）
	"E_007_2501": {"id"}, // 司法冻结失效明细
	"E_007_2601": {"id"}, // 司法协助续行明细
	"E_007_2701": {"id"}, // 解除冻结明细
	"E_007_2801": {"id"}, // 司法协助股东变更明细

	// ─── Phase 6 聚合工具 pcode ───
	// 全量数据角标（4 API）
	"E_003_0331": {},          // 数据角标-司法信息（name）
	"E_007_0871": {},          // 数据角标-企业信息（entName）
	"E_011_0171": {},          // 数据角标-经营信息（entName）
	"E_012_0061": {},          // 数据角标-知识产权（entName）
	// 企业经营详情（8 API，全部 entName）
	"E_007_0701": {}, // 分支机构
	"E_007_0801": {}, // 变更信息
	"E_007_1341": {}, // 标讯企业
	"E_007_4901": {}, // 行政许可_信用中国
	"E_007_3701": {}, // 知识产权出质登记
	"E_007_5001": {}, // 行政处罚_信用中国
	"E_007_1371": {}, // 实际股东
	"E_007_2201": {}, // 注销信息
	// 企业深度画像补充（7 API）
	"E_007_0102": {},          // 企业人员董监高（entName）
	"E_007_0641": {},          // 企业注册地核验（entName）
	"E_007_0961": {},          // 企业坐标（entName）
	"E_007_4101": {},          // 公司曾用名（entName）
	"E_007_4201": {},          // 公司联系方式（entName）
	"E_007_1381": {"companyName"}, // 联系方式V2（companyName）
	// E_017_0201 已存在（企业关联信息查询）
	// 上市公司扩展信息（10 API）
	"E_010_0171": {},          // 银保监行政处罚（entName）
	"E_010_0261": {},          // 高管其他数据（companyName）
	"E_010_0291": {},          // 高管监事会数据（companyName）
	"E_010_0311": {},          // 高管董事会数据（companyName）
	"E_010_0361": {},          // 行业市场比较（companyName）
	"E_010_0371": {},          // 行业分析（companyName）
	"E_010_0391": {},          // 行业财务数据（companyName）
	"E_010_0491": {},          // 龙虎榜明细V2（companyName）
	"E_010_1101": {},          // A股发行信息（companyName）
	"E_010_0801": {},          // 行业代码（companycode）
	// 招商宝分析套件（7 API）
	"E_017_0131": {},          // 招聘信息分析（companyName）
	"E_017_0141": {},          // 专利信息分析（companyName）
	"E_017_0151": {},          // 批量查询数量（companyName）
	"E_017_0171": {},          // 财务健康分析（companyName）
	"E_017_0181": {},          // 投资行为分析（companyName）
	"E_017_0211": {},          // 购地信息分析（companyId；文档名如此，无"股东信息分析"接口）
	"E_017_0221": {},          // 研发实力分析（companyName）
	// 企业发展（5 API）
	"E_018_0011": {"name"},    // 核心团队（name）
	"E_018_0021": {"name"},    // 融资历史（name）
	"E_018_0031": {"name"},    // 竞品信息（name）
	"E_018_0041": {"name"},    // 企业业务（name）
	"E_018_0171": {},          // 企业发展历程（companyId）
	// 投资事件画像（5 API）
	"E_023_0031": {},          // 投资事件（entName）
	"E_023_0041": {},          // 投资趋势统计（entName）
	"E_023_0051": {},          // 合作机构排名（entName）
	"E_023_0061": {},          // 合作机构（entName）
	"E_011_0311": {"name"},    // 投资机构详情（name）
	// 企业评价报告（5 API）
	"E_018_0091": {}, // 创始团队（entName）
	"E_018_0101": {}, // 创新技术竞争力（entName）
	"E_018_0111": {}, // 行业饱和度（province）
	"E_018_0121": {}, // 行业垄断程度（province）
	"E_018_0131": {}, // 行业成长情况（province）

	// ─── 年报详情子表（7 API，级联 ParamKey=id）───
	"E_007_3001": {"id"}, // 年报变更
	"E_007_3101": {"id"}, // 年报股东股权变更
	"E_007_3201": {"id"}, // 年报对外投资
	"E_007_3301": {"id"}, // 年报股东及出资信息
	"E_007_3401": {"id"}, // 年报社保
	"E_007_3501": {"id"}, // 年报网站
	"E_007_3601": {"id"}, // 年报对外提供担保信息

	// ─── 政策 V2（2026-09-22 迁移：E_011_0261/E_011_0271 已弃用，见 catalog meta.deprecated_pcodes）───
	// 详情必填 id（政策主键，取列表返回的 uid）；列表 E_007_1441 全部选填（无条件=全量列表）
	"E_007_1451": {"id"}, // 政策详情V2

	// ─── 产业园区搜索（5 API）───
	"E_007_0971": {},             // 园区列表（parkName）
	"E_007_0981": {"parkId"},     // 园区详情（parkId，通过 keeprisk_query 直调）
	"E_007_0991": {},             // 园区企业列表（entName）
	"E_007_1011": {},             // 园区企业分布（parkName）
	"E_007_1041": {},             // 园区筛选（province/city）
}

func NormalizeKeys(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		key := k
		if a, ok := aliases[k]; ok {
			key = a
		}
		out[key] = v
	}
	return out
}

func SerializeBody(params map[string]any) map[string]any {
	normalized := NormalizeKeys(params)
	out := make(map[string]any, len(normalized))
	for k, v := range normalized {
		out[k] = serializeValue(v)
	}
	return out
}

func serializeValue(v any) any {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case map[string]any, []any:
		return x
	case json.RawMessage:
		var m any
		if json.Unmarshal(x, &m) == nil {
			return m
		}
		return string(x)
	case float64:
		// 避免 fmt.Sprint 产生 "1.5e+07" 科学计数法字符串
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		// 类型化的切片/映射（如 []string、[]map[string]string）必须原样保留，
		// 由 json.Marshal 序列化为 JSON 数组/对象；fmt.Sprint 会产出 "[a b]" 垃圾值。
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Map, reflect.Array:
			return v
		}
		return fmt.Sprint(v)
	}
}

// mirrorSubjectFields 在 catalog 声明了 name（business_params 或 required_one_of）时，
// 用 PDF 主体 entName/companyName 回填 name，使校验与网关兼容层都能通过。
// 对齐 phase4 对 E_007_1251「chainName 同时补 name」的既有做法。
func mirrorSubjectFields(entry *catalog.APIEntry, params map[string]any) {
	if entry == nil || hasValue(params, "name") {
		return
	}
	if !subjectFieldDeclared(entry, "name") {
		return
	}
	var src string
	if hasValue(params, "entName") {
		src = strings.TrimSpace(fmt.Sprint(params["entName"]))
	} else if hasValue(params, "companyName") {
		src = strings.TrimSpace(fmt.Sprint(params["companyName"]))
	}
	if src != "" {
		params["name"] = src
	}
}

func subjectFieldDeclared(entry *catalog.APIEntry, field string) bool {
	for _, p := range entry.BusinessParams {
		if p.Name == field {
			return true
		}
	}
	for _, g := range requiredOneOfGroups(entry) {
		for _, x := range g {
			if x == field {
				return true
			}
		}
	}
	return false
}

// pagingKeys 框架可能统一注入的分页控制参数。
var pagingKeys = []string{"pageNo", "pageSize"}

// dropUndeclaredPaging 剥离接口未声明的分页参数。
//
// 背景（2026-09-18 飞友科技全量实测）：聚合执行器为简化实现，普遍对每个子接口
// 统一注入 pageNo/pageSize，而**大量接口并不声明它们**；网关对未声明参数逐条回
// "未知参数 pageNo，已知字段: id, type…"，聚合工具再把这些 warning 透传给调用方。
// 实测 14 处残留噪音全部来自此路径（年报表子表、尽调 basic 节、产业链画像、
// 园区搜索、企业评价报告等），调用方对这些提示完全无从行动。
//
// 为什么剥离是安全的：pageNo=1 本就是分页默认值；pageSize 对未声明的接口而言
// 会被网关忽略，剥离前后的返回结果一致。
//
// 为什么**只**剥离分页参数：主体字段（name/entName/companyName/chainName…）存在
// 目录少声明的情况，剥离可能把本来可用的调用打断，属不可接受的回归风险。
func dropUndeclaredPaging(entry *catalog.APIEntry, params map[string]any) {
	if entry == nil || len(params) == 0 {
		return
	}
	declared := map[string]bool{}
	for _, p := range entry.BusinessParams {
		declared[strings.TrimSpace(p.Name)] = true
	}
	for _, g := range requiredOneOfGroups(entry) {
		for _, x := range g {
			declared[strings.TrimSpace(x)] = true
		}
	}
	if props, ok := entry.InputSchema["properties"].(map[string]any); ok {
		for k := range props {
			declared[strings.TrimSpace(k)] = true
		}
	}
	if len(declared) == 0 {
		return // 目录无声明信息，保守起见不动
	}
	for _, k := range pagingKeys {
		if _, ok := params[k]; !ok {
			continue
		}
		if declared[k] {
			continue
		}
		delete(params, k)
	}
}

// hasRequiredOneOfValue 检查 required_one_of 单字段是否满足。
// catalog 误标 name 时，PDF 主体 entName/companyName 视为可满足 name。
func hasRequiredOneOfValue(params map[string]any, key string) bool {
	if hasValue(params, key) {
		return true
	}
	if key == "name" {
		return hasValue(params, "entName") || hasValue(params, "companyName")
	}
	return false
}

// requiredOneOfOverrides：catalog required_one_of 缺失（空）时的代码级兜底，
// 依据接口 PDF 明示的必填组。避免空参穿透本地校验打到网关返回 101。
var requiredOneOfOverrides = map[string][][]string{
	// E_003_1501 司法拍卖：PDF 明示 entName/name/courtName/sTime/eTime 必填其一，
	// 但 catalog 未标注 required_one_of，空参曾直接穿透到网关
	"E_003_1501": {{"entName", "name", "courtName", "sTime", "eTime"}},
	// E_011_0251 企业财务状况评分：文档主体字段为 key，但网关同时接受 entName（实测 code=110）；
	// 二者二选一即可（year 为另一必填项，见 requiredFields）。2026-09-17 交叉验证补充。
	"E_011_0251": {{"key", "entName"}},
}

func Validate(entry *catalog.APIEntry, params map[string]any) (normalized map[string]any, errors []string, warnings []string) {
	normalized = NormalizeKeys(params)
	mirrorSubjectFields(entry, normalized)
	dropUndeclaredPaging(entry, normalized)
	oneOfGroups := requiredOneOfGroups(entry)
	if len(oneOfGroups) == 0 {
		oneOfGroups = requiredOneOfOverrides[entry.Pcode]
	}
	for _, groups := range oneOfGroups {
		ok := false
		for _, g := range groups {
			if hasRequiredOneOfValue(normalized, g) {
				ok = true
				break
			}
		}
		if !ok {
			errors = append(errors, fmt.Sprintf("以下字段至少填一个: %s", strings.Join(groups, ", ")))
		}
	}
	for _, field := range requiredFields[entry.Pcode] {
		if !hasValue(normalized, field) {
			errors = append(errors, fmt.Sprintf("缺少必填字段: %s", field))
		}
	}
	known := map[string]struct{}{}
	for _, p := range entry.BusinessParams {
		known[p.Name] = struct{}{}
	}
	// required_one_of 里的字段同样属于"目录已声明"，必须计入 known。
	//
	// 2026-09-18 修正（飞友科技全量实测，52 条噪音告警的唯一来源）：
	// mergeSubjectFields 判定"是否声明 name"时查的是 business_params + required_one_of，
	// 而此处原先只查 business_params，两者对"已声明"的定义不一致 →
	// 镜像注入的 name 紧接着被本函数报成"未知参数 name，已知字段: companyName, number, type…"。
	// 即校验器自己注入、自己告警，调用方完全无从行动。
	// 实测内嵌目录 E_010_0181 等即为此形态：business_params=[companyName,number,type]，
	// required_one_of=[["name"]]。
	for _, groups := range oneOfGroups {
		for _, f := range groups {
			known[f] = struct{}{}
		}
	}
	if len(known) > 0 {
		// 排序后输出：map 遍历顺序随机，会让同一次调用的告警文本在不同进程下不同
		// （2026-09-17 交叉验证发现：8 个进程出现 7 种"已知字段"顺序，影响可复现性与测试稳定性）
		knownList := make([]string, 0, len(known))
		for k := range known {
			knownList = append(knownList, k)
		}
		sort.Strings(knownList)
		unknown := make([]string, 0, len(normalized))
		for k := range normalized {
			if _, ok := known[k]; ok {
				continue
			}
			if _, skip := skipUnknown[k]; skip {
				continue
			}
			unknown = append(unknown, k)
		}
		sort.Strings(unknown)
		preview := knownList
		if len(preview) > 12 {
			preview = preview[:12]
		}
		for _, k := range unknown {
			warnings = append(warnings, fmt.Sprintf("未知参数 %s，已知字段: %s…", k, strings.Join(preview, ", ")))
		}
	}
	// 实测参数要求（区划容器格式、静默忽略的参数等）：与目录声明的 required 同等对待，
	// 阻断型规则进 errors（避免错误数据 + 无效计费），提示型进 warnings。
	ruleErrs, ruleWarns := catalog.CheckParamRules(entry.Pcode, params)
	errors = append(errors, ruleErrs...)
	warnings = append(warnings, ruleWarns...)

	// 主体字段提示（全量审计结论：236 接口主体字段 6 种，name 81/entName 19/companyName 15……）：
	//   - 传了主体字段但一个都不匹配 → 提示（可能 101 或空结果）
	//   - 未传任何主体字段 → 附上本接口要求（帮助模型第一次就传对）
	if msg := catalog.SubjectMismatchWarning(entry, params); msg != "" {
		warnings = append(warnings, msg)
	} else if msg := catalog.SubjectRequirementNote(entry); msg != "" && len(normalized) == 0 {
		warnings = append(warnings, msg)
	}

	// 示例与必填矛盾时给出行动指引，而不是展示一个照抄必失败的示例
	// （2026-09-16 盲测：某接口 example(schoolName) 被 required_one_of(name) 拒绝，
	// 模型在"照抄示例"与"满足必填"之间死循环）。
	//
	// 2026-09-18 收窄（飞友科技全量实测）：该提示讲的是 **example_request 的缺陷**，
	// 与调用方本次入参无关；且它在调用方已满足必填时照样输出
	// （实测 E_010 系列：我们传了 companyName，已通过 name 别名满足 required_one_of，
	//  errors 为空，却仍附"该接口还须提供 name"），对调用方是纯噪音。
	// 现仅在**本次调用确实存在校验错误**时才给该指引——这正是它设计要解决的死循环场景。
	if entry.ExampleRequest != nil && len(errors) > 0 {
		if gaps := exampleRequiredGaps(entry); len(gaps) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"该接口还须提供 %s；example_request 仅列出可选参数，勿直接照抄。",
				strings.Join(gaps, " 或 ")))
		} else if len(normalized) == 0 {
			b, _ := json.Marshal(entry.ExampleRequest)
			warnings = append(warnings, fmt.Sprintf("可参考 example_request: %s", string(b)))
		}
	}
	return normalized, errors, warnings
}

// exampleRequiredGaps 返回 example_request 未满足的必填项（供警告文案）。
// 判定口径与 Validate 一致：required_one_of（含代码级兜底）+ requiredFields。
func exampleRequiredGaps(entry *catalog.APIEntry) []string {
	ex := NormalizeKeys(entry.ExampleRequest)
	oneOfGroups := requiredOneOfGroups(entry)
	if len(oneOfGroups) == 0 {
		oneOfGroups = requiredOneOfOverrides[entry.Pcode]
	}
	var gaps []string
	for _, groups := range oneOfGroups {
		ok := false
		for _, g := range groups {
			if hasRequiredOneOfValue(ex, g) {
				ok = true
				break
			}
		}
		if !ok {
			gaps = append(gaps, strings.Join(groups, "/"))
		}
	}
	for _, field := range requiredFields[entry.Pcode] {
		if !hasValue(ex, field) {
			gaps = append(gaps, field)
		}
	}
	return gaps
}

func requiredOneOfGroups(entry *catalog.APIEntry) [][]string {
	raw := entry.RequiredOneOf
	if raw == nil && entry.InputSchema != nil {
		raw = entry.InputSchema["required_one_of"]
	}
	var groups [][]string
	switch t := raw.(type) {
	case []any:
		for _, item := range t {
			switch g := item.(type) {
			case []any:
				row := make([]string, 0, len(g))
				for _, x := range g {
					row = append(row, fmt.Sprint(x))
				}
				groups = append(groups, row)
			case string:
				groups = append(groups, []string{g})
			}
		}
	case []string:
		for _, s := range t {
			groups = append(groups, []string{s})
		}
	}
	return normalizeNameOnlyGroups(entry, groups)
}

// normalizeNameOnlyGroups 纠正"required_one_of 要求的字段、接口 business_params 全都不声明"的目录不一致。
//
// 已知两类形态（均为 2026-09-18 飞友科技全量实测确认）：
//  1. 组内仅 name 且未声明（61 条，上市公司族 E_010_* 为主）→ 改写为实际主体字段；
//  2. 组内字段全部未声明（如 E_007_1421 上市公司主要人员：required=[entName,companyId]，
//     business_params 只有 companyName，而文档示例正是传 companyName）→ 同样改写，
//     否则按文档传 companyName 的合法调用会被本地校验拦下、从未到达网关。
//
// 仅当**整组字段都未声明**且接口存在已声明的主体字段时才改写；部分声明的不动（无法判定取舍）。
//
// 背景（2026-09-18 飞友科技全量实测）：内嵌目录有 **61 条**这类条目
// （上市公司族 E_010_* 为主，另有 E_007_1251 产业链节点统计、E_017_0091 校企统计），
// 形态统一为 required_one_of=[["name"]]，而 business_params 里根本没有 name
// （实际主体字段是 companyName / chainName / schoolName …）。
//
// 危害：调用方若按本地校验的报错提示去补 `name`，网关会回
// "未知参数 name，已知字段: companyName, number, type…"——**被指向一个错误的字段**。
// 实测直接把 name 传给 E_007_1251/E_017_0091 均触发该告警。
//
// 处理：当组内只有 name 且该接口未声明 name 时，改写为该接口实际声明的主体字段
// （按 entName → companyName → schoolName → chainName → keyword → text 优先级取第一个）。
// 若无任何可替代的主体字段，则原样保留（不臆测）。
func normalizeNameOnlyGroups(entry *catalog.APIEntry, groups [][]string) [][]string {
	if entry == nil || len(groups) == 0 {
		return groups
	}
	// 判定口径只用 business_params，**不含 input_schema.properties**——
	// 目录的 input_schema 是从误标的 required 提取生成的，会继承同样的错误
	// （实测 E_007_1421：properties 里也有 entName/companyId，若计入则永远"已声明"）。
	declared := map[string]bool{}
	for _, p := range entry.BusinessParams {
		declared[strings.TrimSpace(p.Name)] = true
	}
	if declared["name"] {
		return groups // 接口确实声明 name，无需纠正
	}
	// 按业务优先级挑一个实际声明的主体字段
	fallback := ""
	for _, f := range []string{"entName", "companyName", "schoolName", "chainName", "chainHis", "keyword", "text", "universityName"} {
		if declared[f] {
			fallback = f
			break
		}
	}
	if fallback == "" {
		return groups
	}
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		// 整组字段都未被接口声明 → 目录误标，整组替换为实际主体字段
		allUndeclared := true
		for _, f := range g {
			if declared[f] {
				allUndeclared = false
				break
			}
		}
		if allUndeclared {
			out = append(out, []string{fallback})
			continue
		}
		// 组内含未声明的 name（如 ["name","id"]）：换成 fallback 并保留其余项
		row := make([]string, 0, len(g))
		for _, f := range g {
			if f == "name" && !declared["name"] {
				row = append(row, fallback)
				continue
			}
			row = append(row, f)
		}
		out = append(out, row)
	}
	return out
}

func hasValue(params map[string]any, key string) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}