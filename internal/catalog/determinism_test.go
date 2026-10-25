package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// 目录构建必须**确定性**：同一份输入多次构建应产生完全相同的结果。
//
// 背景（2026-09-17 交叉验证发现）：normalizeMetadata 与 applyParamMeta 曾直接 range map
// 遍历（properties / 文档参数），导致"同名择优去重"的胜者与参数追加顺序随运行变化——
// 实测同一二进制 5 次运行得到 5 个不同的元数据哈希，同一份代码在不同进程下给模型
// 不同的参数顺序与内容。
//
// ⚠️ 测试方法要点（前一版测试无效，被交叉验证指出）：
// 早期版本调用 `Default()` 比较两次结果——但 Default() 走 sync.Once 返回**同一指针**，
// 比较必然相等，对非确定性**零校验力**（在旧的非确定代码上也会通过）。
// 必须用 `LoadJSON` 从原始字节**独立构建**才有效。
func TestCatalogBuildIsDeterministic(t *testing.T) {
	raw, err := os.ReadFile("data/full_catalog.json")
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	var first string
	for i := 0; i < 5; i++ {
		c, err := LoadJSON(raw)
		if err != nil {
			t.Fatalf("第 %d 次构建: %v", i+1, err)
		}
		b, err := json.Marshal(c.LatestAPIs())
		if err != nil {
			t.Fatalf("序列化: %v", err)
		}
		sum := sha256.Sum256(b)
		got := fmt.Sprintf("%x", sum[:16])
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("第 %d 次构建结果不一致（%s != %s）：目录构建存在非确定性，"+
				"请检查 overrides.go / parammeta.go 中的 map 遍历是否已排序", i+1, got, first)
		}
	}
}

// 参数追加顺序必须稳定且可预期（按名称排序），便于 diff 与人工审查
func TestDocParamOrderIsSorted(t *testing.T) {
	raw, err := os.ReadFile("data/full_catalog.json")
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	c, err := LoadJSON(raw)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	for _, e := range c.LatestAPIs() {
		var docNames []string
		for _, p := range e.BusinessParams {
			if p.FromDoc {
				docNames = append(docNames, p.Name)
			}
		}
		if len(docNames) < 2 {
			continue
		}
		for i := 1; i < len(docNames); i++ {
			if strings.Compare(docNames[i-1], docNames[i]) > 0 {
				t.Fatalf("%s 的文档参数顺序未排序（%q 在 %q 之后）：会导致构建结果随 map 遍历变化",
					e.Pcode, docNames[i-1], docNames[i])
			}
		}
	}
}

// 同 pcode 的重复目录条目必须**合并**而非覆盖（上游目录对 E_011_0251 有企业/定制两条）。
//
// 背景（2026-09-17 全量核验发现）：原实现 `byPcode[p] = &c.apis[i]` 后写覆盖前写，
// 导致 category 等字段丢失（"企业评分" → 空），且 LenLatest/搜索会出现重复项。
func TestDuplicatePcodesMerged(t *testing.T) {
	raw, err := os.ReadFile("data/full_catalog.json")
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	c, err := LoadJSON(raw)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	// 每个 pcode 在最新版列表中只能出现一次
	seen := map[string]int{}
	for _, e := range c.LatestAPIs() {
		seen[e.Pcode]++
		if seen[e.Pcode] > 1 {
			t.Fatalf("pcode %s 在最新版列表中重复出现（应合并）", e.Pcode)
		}
	}
	// 已知重复条目：字段应被合并保留
	e := c.GetByPcode("E_011_0251")
	if e == nil {
		t.Fatal("未找到 E_011_0251")
	}
	if e.Category == "" || e.ProductLine == "" {
		t.Fatalf("重复条目合并后字段丢失：category=%q product_line=%q", e.Category, e.ProductLine)
	}
	if len(e.BusinessParams) == 0 {
		t.Fatal("合并后业务参数不应为空")
	}
}

// 文档中的"必填其一"组必须被补充到目录（当目录未声明时）。
//
// 背景（2026-09-17 全量核验发现）：42 个接口有"与 X 必填其一"类措辞，其中 9 个目录未声明
// required_one_of，导致空参直达网关拿 101（本地校验无法拦截）。
func TestRequiredGroupsFromDoc(t *testing.T) {
	raw, err := os.ReadFile("data/full_catalog.json")
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	c, err := LoadJSON(raw)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	// E_011_0202 资质证书：文档为 "选填，与 companyId 必填其一"（目录原未声明）
	e := c.GetByPcode("E_011_0202")
	if e == nil {
		t.Fatal("未找到 E_011_0202")
	}
	groups := requiredGroupsOf(e)
	if len(groups) == 0 {
		t.Fatal("资质证书的必填组未被补充（空参会直达网关）")
	}
	found := false
	for _, g := range groups {
		set := map[string]bool{}
		for _, f := range g {
			set[f] = true
		}
		if set["name"] && set["companyId"] {
			found = true
		}
	}
	if !found {
		t.Fatalf("必填组应含 {name, companyId}；实际=%v", groups)
	}
	if !e.RequiredOneOfFromDoc {
		t.Fatal("应标记必填组来自文档提取")
	}
}
