package catalog

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type BusinessParam struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
	// FromDoc 标记该参数来自接口原始文档（目录未列出），供排查与展示区分
	FromDoc bool `json:"from_doc,omitempty"`
}

type APIEntry struct {
	Pcode           string          `json:"pcode"`
	Title           string          `json:"title"`
	Domain          string          `json:"domain"`
	ProductLine     string          `json:"product_line"`
	Category        string          `json:"category"`
	// File 为接口文档文件名（原样保留，供目录导出与人工追溯；运行时不使用）
	File            string          `json:"file,omitempty"`
	BusinessParams  []BusinessParam `json:"business_params"`
	RequiredOneOf   any             `json:"required_one_of"`
	ExampleRequest  map[string]any  `json:"example_request"`
	MarkdownPath    string          `json:"markdown_path"`
	SupersedesPcodes []string       `json:"supersedes_pcodes"`
	IsLatest        bool            `json:"is_latest"`
	SupersededBy    string          `json:"superseded_by"`
	InputSchema     map[string]any  `json:"input_schema"`
	// 以下字段由构建期参数元数据（param_meta.json，源自原始文档）填充
	ExampleFromDoc bool                    `json:"-"` // 示例取自原始文档（权威）
	DocParams      map[string]docParamMeta `json:"-"` // 文档参数表（含枚举值）
	DocPath        string                  `json:"-"` // 原始文档相对路径
	// RequiredOneOfFromDoc 标记必填组来自文档提取（目录未声明时的补充），便于排查
	RequiredOneOfFromDoc bool `json:"-"`
}

type Catalog struct {
	apis        []APIEntry
	// deprecated：上游目录的"已弃用映射"元数据（仅透传/导出，**不再参与任何行为**——
	// 历史编码与未知编码同等待遇，2026-09-22 定标）
	deprecated  map[string]string
	byPcode     map[string]*APIEntry
}

var (
	once     sync.Once
	instance *Catalog
	loadErr  error
	pcodeRe  = regexp.MustCompile(`(?i)^[EP]_\d+_\d+$`)
)

var synonyms = map[string][]string{
	"工商":   {"工商基本信息", "工商股东", "工商信息"},
	"基本信息": {"工商基本信息"},
	"终本":   {"终本案件"},
	"失信":   {"失信人"},
	"被执行":  {"被执行人"},
	"裁判":   {"裁判文书"},
	"立案":   {"立案信息"},
	"筛选":   {"企业筛选", "高级筛选", "校企筛选"},
	"股东":   {"工商股东", "十大股东"},
	"年报":   {"企业年报"},
	"校企":   {"校企筛选", "校企统计", "院校信息"},
	"信易知":  {"信易知", "数据大屏"},
	"招商":   {"以商招商", "科研线索招商", "招商宝"},
	// 校友圈域：能力存在但接口标题不含"校友"，不做同义词路由则关键词检索漏判
	// （2026-09-16 盲测：search_keeprisk_apis("校友") 返回空）。
	"校友": {"校企", "院校信息"},
	"母校": {"校企", "院校信息"},
	"同学": {"校企"},
	"高校": {"院校信息"},
	"大学": {"院校信息", "校企"},
	// 人员维度：「查老板」标题特殊（含"老板"一词），模型常按"高管/董监高/人员"检索而漏判
	// （2026-09-16 二轮盲测 trial1/trial3 指出）。
	"老板":  {"查老板"},
	"高管":  {"查老板"},
	"董监高": {"查老板"},
}

// mergeDuplicatePcodes 合并同 pcode 的重复目录条目。
//
// 背景（2026-09-17 全量核验发现）：上游目录中 E_011_0251 有两条（domain=企业 / domain=定制），
// 原实现 byPcode 后写覆盖前写，导致 category 等信息丢失（"企业评分" → 空）。
// 该接口同时归属两个产品线，正确做法是**把两条的补充信息合并到一条**，而非覆盖。
//
// 合并规则（保守）：仅补齐空缺字段（product_line/category 保留先出现的非空值；
// business_params 与 input_schema.properties 取并集）。被并入的条目从 apis 中移除，
// 避免 LenLatest/搜索出现重复项。
func mergeDuplicatePcodes(c *Catalog) {
	firstIdx := map[string]int{} // pcode → apis 中的首个下标
	kept := make([]APIEntry, 0, len(c.apis))
	for i := range c.apis {
		e := &c.apis[i]
		p := strings.ToUpper(strings.TrimSpace(e.Pcode))
		idx, dup := firstIdx[p]
		if !dup {
			firstIdx[p] = len(kept)
			kept = append(kept, *e)
			continue
		}
		// 合并到已有条目
		dst := &kept[idx]
		if dst.ProductLine == "" {
			dst.ProductLine = e.ProductLine
		}
		if dst.Category == "" {
			dst.Category = e.Category
		}
		if dst.Domain == "" {
			dst.Domain = e.Domain
		}
		// business_params 并集（同 name 不重复追加）
		have := map[string]bool{}
		for _, bp := range dst.BusinessParams {
			have[bp.Name] = true
		}
		for _, bp := range e.BusinessParams {
			if !have[bp.Name] {
				dst.BusinessParams = append(dst.BusinessParams, bp)
				have[bp.Name] = true
			}
		}
		// properties 并集
		if len(e.InputSchema) > 0 {
			if dst.InputSchema == nil {
				dst.InputSchema = map[string]any{}
			}
			if ep, ok := e.InputSchema["properties"].(map[string]any); ok {
				dp, _ := dst.InputSchema["properties"].(map[string]any)
				if dp == nil {
					dp = map[string]any{}
					dst.InputSchema["properties"] = dp
				}
				for k, v := range ep {
					if _, exists := dp[k]; !exists {
						dp[k] = v
					}
				}
			}
		}
	}
	c.apis = kept
}

func LoadJSON(data []byte) (*Catalog, error) {
	var root struct {
		Meta struct {
			DeprecatedPcodes map[string]string `json:"deprecated_pcodes"`
		} `json:"meta"`
		APIs []APIEntry `json:"apis"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		var list []APIEntry
		if err2 := json.Unmarshal(data, &list); err2 != nil {
			return nil, err
		}
		root.APIs = list
	}
	dep := make(map[string]string)
	for k, v := range root.Meta.DeprecatedPcodes {
		dep[strings.ToUpper(k)] = v
	}
	c := &Catalog{apis: root.APIs, deprecated: dep, byPcode: make(map[string]*APIEntry)}
	mergeDuplicatePcodes(c)
	for i := range c.apis {
		if c.apis[i].ProductLine == "" {
			c.apis[i].ProductLine = "企业"
		}
		// JSON 缺省 is_latest 时 Go 为 false；与 Python x.get("is_latest", True) 对齐
		if c.apis[i].SupersededBy != "" {
			c.apis[i].IsLatest = false
		} else if !c.apis[i].IsLatest && c.apis[i].SupersededBy == "" {
			c.apis[i].IsLatest = true
		}
		p := strings.ToUpper(c.apis[i].Pcode)
		c.byPcode[p] = &c.apis[i]
		// 元数据修正层（顺序有依赖）：
		//   1. 原始文档参数元数据（示例校正 + 补充未列出的参数与枚举）
		//   2. 定向修正（overrides.go：必填/提示修正）
		//   3. 键名归一（去空格/同名择优——须在 1、2 之后，文档示例也可能带脏写键）
		applyParamMeta(&c.apis[i])
		applyMetadataFixes(&c.apis[i])
		normalizeMetadata(&c.apis[i])
		// 加载期清理 input_schema 中空的 "description"（PDF 解析产物常见），
		// 避免空描述噪音透传给 LLM；上游 catalog 重建前此地兜底，所有消费点统一干净。
		if c.apis[i].InputSchema != nil {
			if s, ok := stripEmptyDescriptions(c.apis[i].InputSchema).(map[string]any); ok {
				c.apis[i].InputSchema = s
			}
		}
	}
	return c, nil
}

// stripEmptyDescriptions 递归剔除 map/slice 中值为空字符串的 "description" 键。
func stripEmptyDescriptions(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			if k == "description" {
				if s, ok := vv.(string); ok && strings.TrimSpace(s) == "" {
					continue
				}
			}
			out[k] = stripEmptyDescriptions(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = stripEmptyDescriptions(vv)
		}
		return out
	default:
		return v
	}
}

func Default() (*Catalog, error) {
	once.Do(func() {
		instance, loadErr = loadEmbedded()
	})
	return instance, loadErr
}

func normalizePcode(pcode string) string {
	return strings.ToUpper(strings.TrimSpace(pcode))
}

// GetByPcode 按编码取**当前有效**条目；历史/未知编码返回 nil（不再重定向）。
func (c *Catalog) GetByPcode(pcode string) *APIEntry {
	p := normalizePcode(pcode)
	if a, ok := c.byPcode[p]; ok && a.IsLatest {
		return a
	}
	return nil
}

func (c *Catalog) LenLatest() int {
	return len(c.LatestAPIs())
}

func (c *Catalog) LatestAPIs() []APIEntry {
	out := make([]APIEntry, 0, len(c.apis))
	for _, a := range c.apis {
		if a.IsLatest {
			out = append(out, a)
		}
	}
	return out
}

func (c *Catalog) ProductLines() []map[string]any {
	counts := map[string]int{}
	for _, a := range c.apis {
		if !a.IsLatest {
			continue
		}
		pl := a.ProductLine
		if pl == "" {
			pl = "其他"
		}
		counts[pl]++
	}
	type row struct {
		PL    string `json:"product_line"`
		Count int    `json:"api_count"`
	}
	rows := make([]row, 0, len(counts))
	for pl, n := range counts {
		rows = append(rows, row{PL: pl, Count: n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PL < rows[j].PL })
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = map[string]any{"product_line": r.PL, "api_count": r.Count}
	}
	return out
}

func (c *Catalog) Categories(productLine string) []string {
	set := map[string]struct{}{}
	for _, a := range c.apis {
		if !a.IsLatest {
			continue
		}
		if productLine != "" && !strings.Contains(a.ProductLine, productLine) {
			continue
		}
		if a.Category != "" {
			set[a.Category] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) Search(query, category, productLine string, limit int) []APIEntry {
	items := c.LatestAPIs()
	if productLine != "" {
		pl := strings.TrimSpace(productLine)
		filtered := items[:0]
		for _, x := range items {
			if strings.Contains(x.ProductLine, pl) || strings.Contains(x.Domain, pl) || strings.Contains(x.Category, pl) {
				filtered = append(filtered, x)
			}
		}
		items = filtered
	}
	if category != "" {
		cat := strings.TrimSpace(category)
		filtered := items[:0]
		for _, x := range items {
			if strings.Contains(x.Category, cat) {
				filtered = append(filtered, x)
			}
		}
		items = filtered
	}
	if query != "" {
		q := strings.TrimSpace(strings.ToLower(query))
		if pcodeRe.MatchString(q) {
			// 仅当前有效编码可检索命中；历史/未知编码不返回条目（不可见）
			if item := c.GetByPcode(q); item != nil {
				return []APIEntry{*item}
			}
			return nil
		}
		type scored struct {
			s int
			a APIEntry
		}
		var scoredList []scored
		for _, x := range items {
			s := scoreItem(x, q)
			if s > 0 {
				scoredList = append(scoredList, scored{s, x})
			}
		}
		sort.Slice(scoredList, func(i, j int) bool {
			if scoredList[i].s != scoredList[j].s {
				return scoredList[i].s > scoredList[j].s
			}
			return scoredList[i].a.Pcode < scoredList[j].a.Pcode
		})
		items = items[:0]
		for _, sc := range scoredList {
			items = append(items, sc.a)
		}
	}
	if limit < 1 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func scoreItem(item APIEntry, q string) int {
	pcode := strings.ToLower(item.Pcode)
	title := strings.ToLower(item.Title)
	cat := strings.ToLower(item.Category)
	pl := strings.ToLower(item.ProductLine)
	if q == pcode || strings.ToUpper(q) == strings.ToUpper(item.Pcode) {
		return 1000
	}
	if strings.Contains(pcode, q) {
		return 500
	}
	if title == q || strings.Contains(title, q) {
		return 400 - len(title)
	}
	if strings.Contains(pl, q) || strings.Contains(cat, q) {
		return 250
	}
	for kw, titles := range synonyms {
		if strings.Contains(q, kw) {
			for _, t := range titles {
				if strings.Contains(item.Title, t) {
					return 350
				}
			}
		}
	}
	return 0
}

func Slim(item *APIEntry) map[string]any {
	if item == nil {
		return nil
	}
	out := map[string]any{
		"pcode":             item.Pcode,
		"title":             item.Title,
		"product_line":      item.ProductLine,
		"category":          item.Category,
		"business_params":   item.BusinessParams,
		"required_one_of":   item.RequiredOneOf,
		"example_request":   item.ExampleRequest,
		"markdown_path":     item.MarkdownPath,
		"supersedes_pcodes": item.SupersedesPcodes,
	}
	// 参数要求（实测确认的传法规则）：仅对有规则的接口输出，避免全量目录膨胀
	if reqs := ParamRequirements(item.Pcode); len(reqs) > 0 {
		out["param_requirements"] = reqs
	}
	// 标记示例来源：模型据此决定是否照抄（文档示例含主体参数，目录示例常残缺）
	if item.ExampleFromDoc {
		out["example_source"] = "doc"
	}
	return out
}

func (c *Catalog) ResolveAPI(intent, productLine string) map[string]any {
	intent = strings.TrimSpace(intent)
	hits := c.Search(intent, "", productLine, 5)
	if len(hits) == 0 && len(intent) >= 2 {
		// 按 rune 截断：UTF-8 中文 3 字节/字，按字节切片会在字符中间切断产生乱码
		r := []rune(intent)
		hits = c.Search(string(r[:min(4, len(r))]), "", productLine, 5)
	}
	var primary *APIEntry
	if len(hits) > 0 {
		primary = &hits[0]
	}
	alts := make([]map[string]any, 0)
	for i := 1; i < len(hits) && i < 4; i++ {
		alts = append(alts, Slim(&hits[i]))
	}
	hint := "未匹配到接口，试 list_product_lines 或 search_keeprisk_apis。"
	if primary != nil {
		hint = "调用前 validate_keeprisk_params → keeprisk_query（多版本已自动取最新 pcode）。"
	}
	return map[string]any{
		"intent":              intent,
		"product_line_filter": productLine,
		"recommended":         Slim(primary),
		"alternatives":        alts,
		"hint":                hint,
	}
}

func SuggestPcodeFix(c *Catalog, pcode string) string {
	p := normalizePcode(pcode)
	if !pcodeRe.MatchString(p) {
		// 不给示例编码/候选编码：对外输出禁止出现具体接口编码
		return "pcode 格式非法，请检查入参。"
	}
	if c.GetByPcode(p) == nil {
		// 历史编码与未知编码**同等待遇**（2026-09-22 定标：映射已删除，
		// 不再给"已下线/替代接口"提示）；不回显输入（输出禁止出现具体编码）
		return "未知接口编码：请用 search_keeprisk_apis 检索当前可用接口，并按名称调用。"
	}
	return ""
}

// IsPcode 判断输入是否为接口编码形态（[EP]_ddd_dddd）。
// 用于区分「持码直调」与「按名称直调」两条路径。
func IsPcode(s string) bool {
	return pcodeRe.MatchString(strings.ToUpper(strings.TrimSpace(s)))
}

// ResolveTitle 按接口名称解析目录条目（仅在最新版接口中查找）。
//
// 匹配规则（宁拒不猜——这是计费调用入口）：
//  1. 精确匹配（忽略大小写与空格）→ 唯一即命中
//  2. 包含匹配唯一命中 → 采用
//  3. 包含匹配多个 → 返回候选名称列表，ok=false（由调用方列出候选拒调）
//  4. 无匹配 → ok=false
func (c *Catalog) ResolveTitle(name string) (item *APIEntry, candidates []string, ok bool) {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	}
	nq := norm(name)
	if nq == "" {
		return nil, nil, false
	}
	var partial []*APIEntry
	for i := range c.apis {
		a := &c.apis[i]
		if !a.IsLatest {
			continue
		}
		nt := norm(a.Title)
		if nt == nq {
			return a, nil, true
		}
		if strings.Contains(nt, nq) {
			partial = append(partial, a)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil, true
	}
	if len(partial) > 1 {
		titles := make([]string, 0, len(partial))
		for _, a := range partial {
			titles = append(titles, a.Title)
		}
		sort.Strings(titles)
		return nil, titles, false
	}
	return nil, nil, false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}