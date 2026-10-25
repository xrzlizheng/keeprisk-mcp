package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// param_meta.json 由 eval/merge_param_meta.py 从接口原始文档生成（构建期产物）。
//
// 背景（2026-09-17）：目录 full_catalog.json 的示例与参数表存在残缺——
// ①示例常只含筛选项片段、缺主体参数（模型照抄必失败）；
// ②未列出文档中真实存在的参数（如 schoolName/labelList）与枚举值。
// 原始文档（markdown/.../[pcode]标题.md）才是权威来源，故提取后在此合并。
//
// 更新方式：`python3 eval/merge_param_meta.py` 后重新编译。
//
//go:embed data/param_meta.json
var embeddedParamMeta []byte

type docParamMeta struct {
	Type     string   `json:"type"`
	Required string   `json:"required"`
	Desc     string   `json:"desc"`
	Note     string   `json:"note"` // 文档参数表"说明"列（常含关键语义，如"精确匹配，置顶返回"）
	Enum     []string `json:"enum"`
}

type apiParamMeta struct {
	ExampleRequest map[string]any           `json:"example_request"`
	Params         map[string]docParamMeta  `json:"params"`
	// RequiredGroups：文档中的"必填其一"组（如 [["name","companyId"]]）。
	// 仅在目录**未声明** required_one_of 时作为补充来源——目录声明更权威（可能含多组）。
	RequiredGroups [][]string `json:"required_groups"`
	DocPath        string     `json:"doc_path"`
}

type paramMetaFile struct {
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
	APIs map[string]apiParamMeta `json:"apis"`
}

var (
	paramMetaOnce sync.Once
	paramMeta     *paramMetaFile
)

// applyParamMeta 合并文档参数元数据到条目：
//  1. example_request：文档示例优先（含主体参数，修复目录示例残缺）
//  2. business_params：仅追加目录**未列出**的参数（不覆盖既有提示，保守）
//  3. DocParams：整体保留供 get_keeprisk_api 输出（含枚举值）
func applyParamMeta(e *APIEntry) {
	pm := loadParamMeta()
	if pm == nil {
		return
	}
	m, ok := pm.APIs[e.Pcode]
	if !ok {
		return
	}
	// 1) 文档示例优先：目录示例残缺（缺主体参数）是模型照抄失败的主因
	if len(m.ExampleRequest) > 0 {
		e.ExampleRequest = m.ExampleRequest
		e.ExampleFromDoc = true
	}
	// 2) 补充目录未列出的参数（含类型/必填/描述），不覆盖既有提示
	//
	// ⚠️ 必须按名称排序后再追加：文档参数存于 map，直接 range 会因 Go map 遍历顺序随机
	// 导致每次构建的参数顺序不同（实测同一二进制 5 次运行得到 5 个不同的元数据哈希，
	// 2026-09-17 由交叉验证发现）。
	declared := map[string]bool{}
	for _, p := range e.BusinessParams {
		declared[p.Name] = true
	}
	docNames := make([]string, 0, len(m.Params))
	for name := range m.Params {
		docNames = append(docNames, name)
	}
	sort.Strings(docNames)
	for _, name := range docNames {
		meta := m.Params[name]
		if name == "" || declared[name] {
			continue
		}
		// hint 组成：说明 + 必填性 + 文档"说明"列语义 + 枚举摘要
		// （"说明"列常含关键语义，如 almaMater 的"精确匹配，母校置顶返回"）
		var parts []string
		if meta.Desc != "" {
			parts = append(parts, meta.Desc)
		} else if meta.Type != "" {
			parts = append(parts, meta.Type)
		}
		if meta.Required != "" {
			parts = append(parts, meta.Required)
		}
		if meta.Note != "" && meta.Note != meta.Required {
			parts = append(parts, meta.Note)
		}
		hint := strings.Join(parts, "；")
		if len(meta.Enum) > 0 {
			hint = fmt.Sprintf("%s（可选值：%s）", hint, joinEnumShort(meta.Enum))
		}
		e.BusinessParams = append(e.BusinessParams, BusinessParam{Name: name, Hint: hint, FromDoc: true})
	}
	// 3) 完整文档参数元数据（供 get_keeprisk_api 输出枚举）
	if len(m.Params) > 0 {
		e.DocParams = m.Params
	}
	// 4) "必填其一"组：仅当目录未声明 required_one_of 时补充
	//    （目录声明更权威，可能含多组；此处只补空缺，避免覆盖）
	if len(m.RequiredGroups) > 0 && len(requiredGroupsOf(e)) == 0 {
		groups := make([][]string, 0, len(m.RequiredGroups))
		for _, g := range m.RequiredGroups {
			if len(g) >= 2 {
				groups = append(groups, g)
			}
		}
		if len(groups) > 0 {
			setRequiredOneOf(e, groups)
			e.RequiredOneOfFromDoc = true
		}
	}
	if m.DocPath != "" {
		e.DocPath = m.DocPath
	}
}

// joinEnumShort 枚举值摘要（最多 8 项，避免 hint 过长）
func joinEnumShort(vals []string) string {
	if len(vals) > 8 {
		vals = append(append([]string{}, vals[:8]...), "…")
	}
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += "、"
		}
		out += v
	}
	return out
}

func loadParamMeta() *paramMetaFile {
	paramMetaOnce.Do(func() {
		var f paramMetaFile
		if err := json.Unmarshal(embeddedParamMeta, &f); err == nil {
			paramMeta = &f
		}
	})
	return paramMeta
}
