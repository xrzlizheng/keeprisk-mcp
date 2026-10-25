package catalog

import "testing"

// 名称直调（ResolveTitle）是 pcode 脱敏后 Agent 调用未封装领域接口的唯一路径，
// 匹配规则必须"宁拒不猜"：精确 > 唯一包含 > 多候选拒调。
func TestResolveTitle(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}

	t.Run("精确匹配", func(t *testing.T) {
		for _, name := range []string{"院校信息", "校企筛选V2", "校企统计"} {
			item, cands, ok := c.ResolveTitle(name)
			if !ok {
				t.Fatalf("%s 应精确命中, cands=%v", name, cands)
			}
			if item.Title != name {
				t.Fatalf("命中 %s, want %s", item.Title, name)
			}
			if item.Pcode == "" {
				t.Fatalf("%s 解析结果缺少 pcode", name)
			}
		}
	})

	t.Run("忽略大小写与空格", func(t *testing.T) {
		item, _, ok := c.ResolveTitle("院校 信息")
		if !ok || item.Title != "院校信息" {
			t.Fatalf("「院校 信息」应命中院校信息, ok=%v", ok)
		}
	})

	t.Run("唯一包含匹配", func(t *testing.T) {
		item, _, ok := c.ResolveTitle("院校")
		if !ok {
			t.Fatal("「院校」应唯一命中院校信息")
		}
		if item.Title != "院校信息" {
			t.Fatalf("命中 %s, want 院校信息", item.Title)
		}
	})

	t.Run("多候选拒调", func(t *testing.T) {
		item, cands, ok := c.ResolveTitle("筛选")
		if ok {
			t.Fatalf("「筛选」歧义必须拒调, 却命中了 %s", item.Title)
		}
		if len(cands) < 2 {
			t.Fatalf("应返回候选列表, got %v", cands)
		}
		for _, cand := range cands {
			if cand == "" {
				t.Fatal("候选列表含空标题")
			}
		}
	})

	t.Run("未知名称", func(t *testing.T) {
		item, cands, ok := c.ResolveTitle("不存在接口XYZ")
		if ok || cands != nil {
			t.Fatalf("未知名称应 ok=false 且无候选, got %v %v", item, cands)
		}
	})

	t.Run("空名称", func(t *testing.T) {
		if _, _, ok := c.ResolveTitle("  "); ok {
			t.Fatal("空名称应拒绝")
		}
	})
}

func TestIsPcode(t *testing.T) {
	// pcodeRe 为宽松形态（[EP]_数字_数字），与 SuggestPcodeFix 的判定保持一致
	yes := []string{"E_007_0501", "e_006_0011", "  P_003_0361  ", "E_017_0091", "E_07_0501"}
	no := []string{"院校信息", "校企筛选V2", "", "E0070501", "筛选", "E_007_"}
	for _, s := range yes {
		if !IsPcode(s) {
			t.Errorf("IsPcode(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsPcode(s) {
			t.Errorf("IsPcode(%q) = true, want false", s)
		}
	}
}
