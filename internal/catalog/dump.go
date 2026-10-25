package catalog

import (
	"encoding/json"
	"sort"
)

// EffectiveJSON 导出**运行时有效目录**（服务实际加载并对外服务的形态）：
// 已合并重复 pcode、剔除已弃用条目、应用 param_meta/overrides/归一化之后的结果。
//
// 用途：`cmd/catalog-dump` 据此再生成仓库根目录的 data/full_catalog.json
// （评测 harness 读取的镜像副本），使其与服务真实行为一致；两文件的语义一致性
// 由 catalog_sync_test.go 守护。
//
// 输出结构与 full_catalog.json 对齐：{"meta":{...},"apis":[...]}。
func (c *Catalog) EffectiveJSON() ([]byte, error) {
	latest := c.LatestAPIs()
	dep := make(map[string]string, len(c.deprecated))
	for k, v := range c.deprecated {
		dep[k] = v
	}
	out := map[string]any{
		"meta": map[string]any{
			"active_count":      len(latest),
			"deprecated_count":  len(dep),
			"deprecated_pcodes": dep,
			"note": "由 internal/catalog 运行时有效目录导出（服务真实行为为准），勿手改；" +
				"再生成：go run ./cmd/catalog-dump -o data/full_catalog.json（一致性由 catalog_sync_test.go 守护）",
		},
		"apis": latest,
	}
	// 确定性输出：map 键排序由 encoding/json 保证，apis 顺序沿用目录文件顺序
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// EffectiveFingerprint 返回有效目录（apis + 弃用映射）的规范化 JSON，用于一致性比对。
func (c *Catalog) EffectiveFingerprint() ([]byte, error) {
	latest := c.LatestAPIs()
	keys := make([]string, 0, len(c.deprecated))
	for k := range c.deprecated {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dep := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		dep = append(dep, k, c.deprecated[k])
	}
	return json.Marshal(map[string]any{
		"apis":              latest,
		"deprecated_count":  len(c.deprecated),
		"deprecated_pcodes": dep,
	})
}
