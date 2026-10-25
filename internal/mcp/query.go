package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
	"github.com/xrzlizheng/keeprisk-mcp/internal/cache"
	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
	"github.com/xrzlizheng/keeprisk-mcp/internal/keeprisk"
	"github.com/xrzlizheng/keeprisk-mcp/internal/params"
	"github.com/xrzlizheng/keeprisk-mcp/internal/quota"
)

// resolveAPIRef 解析「pcode 或接口名称」输入，供 keeprisk_query /
// get_keeprisk_api / validate_keeprisk_params 三个入口共用。
//
// 背景：对外输出不暴露接口编码（脱敏），search_keeprisk_apis 检索到的
// 接口名称是 Agent 定位接口的唯一合法句柄；若调用入口只认 pcode，
// 未封装成专用工具的领域（如校企/院校）会陷入"名称可见但无法调用"的死胡同。
//
// 返回 verr 非空表示本地校验失败（未触达网关，不计费）。
// 历史编码（已下线）在此本地拒绝：不自动升级、不回显编码，引导改用当前接口名称。
func (s *Services) resolveAPIRef(input string) (item *catalog.APIEntry, verr string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, "请提供 pcode 或接口名称（search_keeprisk_apis 返回的 title）。"
	}
	if catalog.IsPcode(input) {
		// 历史编码 / 未知编码 / 非法形态：本地拒绝（不发网关、不计费）
		if fix := catalog.SuggestPcodeFix(s.Cat, input); fix != "" {
			return nil, fix
		}
		it := s.Cat.GetByPcode(input)
		if it == nil {
			return nil, "未知接口编码：请用 search_keeprisk_apis 检索当前可用接口，并按名称调用。"
		}
		return it, ""
	}
	t, cands, ok := s.Cat.ResolveTitle(input)
	if !ok {
		if len(cands) > 0 {
			return nil, "接口名称「" + input + "」匹配到多个接口：" + strings.Join(cands, " / ") + "。请使用完整名称重试。"
		}
		return nil, "未知接口名称「" + input + "」，请先 search_keeprisk_apis 检索完整名称。"
	}
	return t, ""
}

func (s *Services) QueryImpl(ctx context.Context, pcode string, rawParams map[string]any) string {
	item, verr := s.resolveAPIRef(pcode)
	if verr != "" {
		return mustJSON(map[string]any{"error": "validation", "message": verr})
	}
	body, errs, warns := params.Validate(item, rawParams)
	// 主体字段提示（未阻断但值得模型知道）：补充本接口要求的主体字段
	if msg := catalog.SubjectMismatchWarning(item, rawParams); msg != "" {
		warns = append(warns, msg)
	}
	if len(errs) > 0 {
		return mustJSON(map[string]any{
			"error":           "validation",
			"pcode_used":      item.Pcode,
			"errors":          errs,
			"warnings":        warns,
			"example_request": item.ExampleRequest,
		})
	}

	// 缓存命中检查（命中不计费、不调网关）
	// 缓存默认按 AppCode 隔离（安全默认，防伪造 AppCode 跨租户读旁路）；
	// 仅显式 KEEPRISK_CACHE_PER_APPCODE=false 才全局共享。
	appCodeForCache := ""
	if s.Cache != nil && s.Cache.IsPerAppCode() {
		appCodeForCache = auth.AppCodeFromContext(ctx)
	}
	// 分页归一（2026-09-20）：pageNo=1 的列表查询，缓存 key 忽略 pageSize。
	// 不同工具对同一接口注入的 pageSize 各异（实测 E_007_0601 同主体被注入 5/10/20
	// 三种、一回合重复计费 3 次）。归一后：缓存按 (pcode, 其余参数) 存储最大窗口，
	// 命中且窗口 >= 请求条数时裁剪返回；窗口不足则真实调用并以更大窗口覆盖。
	reqPage, reqSize := pageWindow(body)
	cacheBody := body
	if reqPage == 1 && reqSize > 0 {
		cb := make(map[string]any, len(body))
		for k, v := range body {
			cb[k] = v
		}
		delete(cb, "pageSize")
		cacheBody = cb
	}
	cacheKey := cache.KeyWithAppCode(item.Pcode, cacheBody, appCodeForCache)
	if cached, ok := s.Cache.Get(cacheKey); ok {
		// 缓存条目两种格式：新格式 {"sz":N,"resp":"<原响应JSON>"}（分页归一后写入），
		// 旧格式即原响应 JSON。**必须先判格式再使用**——包装体本身也是合法 JSON，
		// 若把它当旧格式解析返回，调用方会拿到 {"sz":..,"resp":..} 而非业务响应
		// （2026-09-20 实测：judicial_deep_dive 因此把缓存命中当网关故障）。
		var w struct {
			Sz   int    `json:"sz"`
			Resp string `json:"resp"`
		}
		_ = json.Unmarshal(cached, &w)
		// 包装条目恒有 sz>0（写入时 reqSize>0）；仅凭 resp 判会误伤含 resp 字段的旧格式业务响应
		if w.Resp != "" && w.Sz > 0 {
			// 新格式：非分页查询直接整包用；分页查询要求缓存窗口覆盖请求条数。
			// 窗口不足时穿透到真实调用（不把包装体当响应）。
			serveOK := reqPage != 1 || reqSize <= 0 || w.Sz >= reqSize
			if serveOK {
				var parsed map[string]any
				if json.Unmarshal([]byte(w.Resp), &parsed) == nil {
					if reqPage == 1 && reqSize > 0 {
						sliceListPayload(parsed, reqSize)
					}
					parsed["_cache_hit"] = true
					parsed["pcode_used"] = item.Pcode
					rewriteStaleBillingMeta(parsed)
					return mustJSON(parsed)
				}
				// resp 损坏：穿透到真实调用
			}
		} else {
			// 旧格式（未包装窗口）：整包使用，无法裁剪
			var parsed map[string]any
			if json.Unmarshal(cached, &parsed) == nil {
				parsed["_cache_hit"] = true
				parsed["pcode_used"] = item.Pcode
				rewriteStaleBillingMeta(parsed)
				return mustJSON(parsed)
			}
		}
		// 窗口不足 / 缓存损坏：降级到真实调用，并以本次更大窗口覆盖缓存
	}

	// 配额/限流检查（每次网关调用计 1 次计费单位）
	appCode, qerr := s.Quota.AcquireFromContext(ctx)
	if qerr != nil {
		out := map[string]any{
			"error":      "quota",
			"pcode_used": item.Pcode,
			"message":    qerr.Error(),
		}
		if quota.IsLimitError(qerr) {
			out["retry_after_hint"] = "请稍后重试或申请提额。"
		}
		return mustJSON(out)
	}
	defer s.Quota.Release(appCode)

	result, err := s.KR.CallDataAPI(ctx, item.Pcode, body)
	if err != nil {
		switch e := err.(type) {
		case *keeprisk.ConfigError:
			return mustJSON(map[string]any{"error": "config", "message": e.Msg})
		case *keeprisk.APIError:
			out := map[string]any{"error": "api", "message": e.Msg, "pcode_used": item.Pcode}
			if e.Code != nil {
				out["code"] = e.Code
			}
			if e.Payload != nil {
				out["payload"] = e.Payload
			}
			return mustJSON(out)
		default:
			return mustJSON(map[string]any{"error": "api", "message": err.Error(), "pcode_used": item.Pcode})
		}
	}
	// 补 pcode_used 便于审计（出口脱敏会移除该键）
	merged := map[string]any{"pcode_used": item.Pcode}
	for k, v := range result {
		merged[k] = v
	}
	// 校验警告必须随成功响应返回：否则模型无从得知"参数被上游忽略"等静默问题
	// （如区划平铺传参返回全国数据却看起来筛选成功）。
	if len(warns) > 0 {
		merged["warnings"] = warns
	}
	// 无数据标注：网关 code=110（查询成功、无记录）是**正常成功响应**，不是错误。
	// 但响应形态为 {"code":"110","data":null}，模型容易把 data:null 读成"失败/服务异常"，
	// 进而重试、改参或编造结论。此处显式标注，并给出"如何区分主体不存在 vs 该主体无此类记录"。
	attachEmptyResultNote(merged)

	// 仅缓存成功响应（code=100/110 或 无 error 字段）
	// per-pcode 差异化 TTL：舆情类(E_009_) HintTTL=0 不缓存；
	// 被执行/失信等高变化接口用 60s 而非全局 TTL，避免陈旧风控数据。
	if shouldCache(result) {
		if b, jerr := json.Marshal(merged); jerr == nil {
			// 分页归一存储：包一层窗口大小，后续命中可判断"缓存窗口是否覆盖请求条数"
			if reqPage == 1 && reqSize > 0 {
				if wb, werr := json.Marshal(map[string]any{"sz": reqSize, "resp": string(b)}); werr == nil {
					b = wb
				}
			}
			s.Cache.SetWithTTL(cacheKey, b, cache.HintTTL(item.Pcode, s.Cache.TTL()))
		}
	}
	return mustJSON(merged)
}

// pageWindow 提取 body 中的 pageNo/pageSize（容忍字符串数值），无则返回 (0,0)。
func pageWindow(body map[string]any) (int, int) {
	pn, ok1 := anyToInt(body["pageNo"])
	ps, ok2 := anyToInt(body["pageSize"])
	if !ok1 || !ok2 {
		return 0, 0
	}
	return pn, ps
}

// sliceListPayload 将响应中的业务列表数组裁剪到前 n 条（原地修改）。
// 只认常见载荷键（items/list/rows/records），在 data 节点内外两层查找；
// total 等计数保持原值（它表示全量记录数，与本页条数无关）。
func sliceListPayload(parsed map[string]any, n int) {
	if n <= 0 {
		return
	}
	cut := func(m map[string]any) {
		for _, k := range []string{"items", "list", "rows", "records"} {
			if lst, ok := m[k].([]any); ok && len(lst) > n {
				m[k] = lst[:n]
			}
		}
	}
	cut(parsed)
	if d, ok := parsed["data"].(map[string]any); ok {
		cut(d)
		if d2, ok := d["data"].(map[string]any); ok {
			cut(d2)
		}
	}
}

// rewriteStaleBillingMeta 缓存命中响应的计费元数据改写：
// 命中缓存意味着本次未触达网关、未计费，但缓存体里的 charge/orderNo/time
// 是首次调用的原值——按响应字段对账会多算成本，陈旧 orderNo 会被误当新流水号。
func rewriteStaleBillingMeta(parsed map[string]any) {
	if ch, ok := parsed["charge"].(bool); ok && ch {
		parsed["charge"] = false
		parsed["_charge_original"] = true
	}
	delete(parsed, "orderNo")
	delete(parsed, "time")
}

// isEmptyResult 判定网关响应是否属于"查询成功但无数据"。
//
// 识别两类形态（实测归纳）：
//  1. code=110（网关明示"查询成功，无数据"），data 通常为 null；
//  2. code=100（成功）但没有任何业务载荷（data 为 null / 空 map / 空数组）。
//
// 注意：**不能只看 data==null 就判定无数据**——错误响应（如 code=500）的 data 也为 null，
// 需先排除带 error 字段或非成功 code 的情况，否则会把故障误标成"正常无数据"。
//
// 2026-09-18 修正（飞友科技全量实测）：旧实现只看 `data.list`，有两个漏判：
//  1. 载荷键为 items / nodes / links 的接口完全不被识别；
//  2. 这些接口的内层载荷节点形如 {code:100, items:[...], total:2}，自身没有 "data" 键，
//     旧实现的 `switch m["data"]` 命中 `case nil` → 直接判空。
//
// 实测后果：E_003_0601(法院公告)、E_003_0901(开庭公告)、E_007_0201(实际控制权)、
// E_007_0301(最终受益人) 带 2~5 条 items 却被标 `_empty_result:true` 并附
// "此结论可采信，无需重试"文案——其中法院公告正是尽调里唯一的红级风险来源，
// 会直接导致"无风险记录"的错误结论。
//
// 现改为**证据式**判定：只要节点内存在任一业务载荷证据即认定非空；只有完全找不到证据才判空。
// 证据的判定刻意偏向"有数据"——漏标无数据只是少一条提示，误标无数据会让 Agent 跳过排查。
func isEmptyResult(m map[string]any) bool {
	if m == nil {
		return false
	}
	// 错误响应不算（error 字段存在，或 code 非 100/110）
	if _, hasErr := m["error"]; hasErr {
		return false
	}
	code := codeOf(m["code"])
	if code != 100 && code != 110 {
		return false
	}
	// 载荷证据优先于 code：上游存在 code=110 却携带数据的情况（实测 E_003_0411 即
	// code=110 且 charge=true），此时不能声称无记录。
	return !hasPayloadEvidence(m, 0)
}

// payloadMetaKeys 不参与"有无载荷"判定的元数据键。
//
// 四类：
//  1. 协议字段（code/message/orderNo/time/charge）；
//  2. 分页控制字段（pageNo/pageSize —— 恒正数，不排除会被当成"有数据"）；
//  3. 上游用来表达空结果的状态包装（status/msg）；
//  4. **框架自己附加的元数据**（pcode_used）——QueryImpl 为便于审计会给每个响应加
//     `pcode_used`，它是接口编码字符串；不排除会被当成"业务字符串载荷"，
//     导致真空响应（code=110/data=null）被判成 has_data=true
//     （2026-09-18 实测于 annual_report_investments.financial_annual）。
//
// 注意 `total` 不在此列：正的 total 正是"有数据"的证据，需参与判定。
var payloadMetaKeys = map[string]struct{}{
	"message": {}, "msg": {}, "code": {}, "orderNo": {}, "orderno": {},
	"param": {}, "time": {}, "charge": {}, "success": {}, "warnings": {},
	"pageNo": {}, "pageSize": {}, "pageNum": {}, "page_num": {},
	"status": {}, "errMsg": {}, "errmsg": {},
	"pcode_used": {}, "pcode": {},
}

// placeholderStrings 占位字符串：上游用它们表达"该期字段无值"或"本次查询无数据"。
//
// 实测来源：E_007_0941 的 2022 期为 15 个字段全 "-" 的空壳对象（却 charge=true）；
// E_010 系列无数据时 data={"msg":"查询数据为空","status":100}。
// 若不识别占位符，这类响应会被判为"有数据"，Agent 会读成"各项指标均为空/异常"。
var placeholderStrings = map[string]struct{}{
	"": {}, "-": {}, "--": {}, "null": {}, "NULL": {}, "N/A": {}, "n/a": {}, "无": {},
	"查询数据为空": {}, "查询成功，无数据": {}, "无数据": {}, "暂无数据": {}, "暂无": {},
}

// hasPayloadEvidence 判断节点内是否存在业务载荷证据（递归，深度受限）。
//
// 证据形态（任一即可）：
//   - 非空数组（items / list / nodes / links / 任意键）
//   - 非零数值（含正的 total），分页控制字段除外
//   - 非占位字符串
//   - 布尔值（真/假都算：全 false 往往正是该接口的业务结论，如"各项标签均不满足"）
func hasPayloadEvidence(m map[string]any, depth int) bool {
	if depth > 5 {
		return false
	}
	for k, v := range m {
		if strings.HasPrefix(k, "_") {
			continue
		}
		if _, isMeta := payloadMetaKeys[k]; isMeta {
			continue
		}
		switch t := v.(type) {
		case []any:
			if len(t) > 0 {
				return true
			}
		case map[string]any:
			if hasPayloadEvidence(t, depth+1) {
				return true
			}
		case string:
			if _, isPlaceholder := placeholderStrings[strings.TrimSpace(t)]; !isPlaceholder {
				return true
			}
		case float64:
			if t != 0 {
				return true
			}
		case int:
			if t != 0 {
				return true
			}
		case json.Number:
			if f, err := t.Float64(); err == nil && f != 0 {
				return true
			}
		case bool:
			return true
		}
	}
	return false
}

// codeOf 宽松解析 code（string / float64 / int）
func codeOf(v any) int {
	switch t := v.(type) {
	case string:
		n := 0
		for _, c := range t {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	case float64:
		return int(t)
	case int:
		return t
	}
	return 0
}

// attachEmptyResultNote 为"无数据"响应附加显式标注，避免模型误读为失败。
//
// 为什么必须显式标注：无数据响应形如 {"code":"110","data":null,"message":"查询成功，无数据"}，
// 模型看到 data:null 常推断为"服务异常/查询失败"，进而重试、换参或直接下结论。
// 而这类结果**本身往往是有价值的业务结论**（如"无被执行记录"是正面信号）。
func attachEmptyResultNote(m map[string]any) {
	if !isEmptyResult(m) {
		return
	}
	if _, exists := m["_empty_result"]; exists {
		return
	}
	m["_empty_result"] = true
	// 不覆盖调用方已有的 _note（部分工具会写自己的说明）
	if _, exists := m["_note"]; !exists {
		m["_note"] = "查询成功但**无记录**（网关 code=110/空 data，未计费）：这是正常的成功响应，" +
			"**不是错误、不是服务故障，无需重试或改参**。含义是该主体在此接口下没有对应数据记录；" +
			"若需确认主体本身是否存在，请用 resolve_entity / enterprise_basic_profile 核验；" +
			"在风险排查场景中，「无记录」通常是有意义的正面结论（如无被执行/无失信）。"
	}
	if _, exists := m["_next"]; !exists {
		m["_next"] = []string{
			"用 resolve_entity 或 enterprise_basic_profile 确认主体是否存在（排除名称写法差异）",
			"若主体存在：该接口无记录即为结论，可直接采信，不必换参重试",
			"若需更宽口径，可换相邻接口（如 失信人 ↔ 被执行人 ↔ 终本案件）交叉核对",
		}
	}
}

// shouldCache 判定网关响应是否值得缓存
//
// 网关成功 code=100 / 110；非错误但有 message 提示（如 "无记录"）也缓存（数据稳定）。
// 错误响应不缓存（避免缓存瞬时错误）。
func shouldCache(result map[string]any) bool {
	if result == nil {
		return false
	}
	if _, hasErr := result["error"]; hasErr {
		return false
	}
	if _, hasMsg := result["message"]; hasMsg {
		// 有 message 但无 error，可能是网关提示（如 "无记录"），按非错误缓存
	}
	return true
}

func mustJSON(v any) string {
	// 默认紧凑 JSON（节省 LLM token 约 30%）；KEEPRISK_PRETTY_JSON=true 时输出缩进（便于人工排查）
	pretty := strings.EqualFold(strings.TrimSpace(os.Getenv("KEEPRISK_PRETTY_JSON")), "true")
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		b, _ = json.Marshal(map[string]any{"error": "json", "message": err.Error()})
	}
	return string(b)
}

// applyQueryMeta 将 QueryImpl 原始响应中附加的网关层元数据透传到聚合工具输出。
//
// 背景：resolve_entity 等聚合工具会解析 QueryImpl 返回的 JSON 并重建输出 map，
// 此过程丢弃了 QueryImpl 附加的 _cache_hit / pcode_used / warnings / _redirect，
// 导致调用方无法判断本次响应是否命中缓存（是否计费）、实际生效的 pcode 是什么。
// 仅当聚合工具输出中不存在同名键时才写入（不覆盖聚合工具自身设置的值）。
func applyQueryMeta(out, parsed map[string]any) {
	if out == nil || parsed == nil {
		return
	}
	for _, k := range []string{"_cache_hit", "pcode_used", "warnings", "_redirect"} {
		if v, ok := parsed[k]; ok {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
}

// resolveTotalHits 提取 E_007_1331 响应中的 total。
//
// 真实响应结构（2026-09-04 对生产网关实测）：total 位于 data.total 嵌套层级，
// 原实现读顶层 parsed["total"] 恒为缺失，导致 total_hits 恒为 0、与
// candidates_shown 矛盾，误导 Agent 判断候选完整性。
// 提取顺序：data.total → 顶层 total → 兜底 len(candidates)（已知下界，
// 与 candidates_shown 一致，不再虚构 0）。
func resolveTotalHits(parsed map[string]any, fallback int) int {
	if data, ok := parsed["data"].(map[string]any); ok {
		if v, ok := anyToInt(data["total"]); ok {
			return v
		}
	}
	if v, ok := anyToInt(parsed["total"]); ok {
		return v
	}
	return fallback
}

// anyToInt 宽松地把 JSON 反序列化出的数值转 int（float64/int/json.Number/数字字符串）。
func anyToInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}
