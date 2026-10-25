package params

import (
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// 2026-09-16 盲测：某接口 example 与 required_one_of 互相矛盾，模型照抄示例必然被拒。
// 修正后（catalog/overrides.go）：示例可满足必填；仍无法满足的接口则给出行动指引警告。
func TestValidateExampleConsistency(t *testing.T) {
	c, err := catalog.Default()
	if err != nil {
		t.Fatalf("加载目录: %v", err)
	}

	t.Run("校企统计 schoolName 单传可通过本地校验", func(t *testing.T) {
		e := c.GetByPcode("E_017_0091")
		if e == nil {
			t.Fatal("未找到 E_017_0091")
		}
		_, errs, warns := Validate(e, map[string]any{"schoolName": "中国科学技术大学"})
		if len(errs) != 0 {
			t.Fatalf("schoolName 单传应通过, errors=%v", errs)
		}
		for _, w := range warns {
			if strings.Contains(w, "勿直接照抄") {
				t.Fatalf("示例与必填已一致，不应再提示照抄风险: %v", warns)
			}
		}
	})

	t.Run("校企统计 空参给出明确必填", func(t *testing.T) {
		e := c.GetByPcode("E_017_0091")
		_, errs, _ := Validate(e, map[string]any{})
		if len(errs) == 0 {
			t.Fatal("空参应报缺必填")
		}
		joined := strings.Join(errs, " ")
		if !strings.Contains(joined, "schoolName") {
			t.Fatalf("必填提示未指向真实参数 schoolName: %v", errs)
		}
		if strings.Contains(joined, "name") && !strings.Contains(joined, "schoolName") {
			t.Fatalf("仍在要求错误的 name 字段: %v", errs)
		}
	})

	t.Run("文档示例已修复主体缺失（不再提示照抄风险）", func(t *testing.T) {
		// E_007_0701 分支机构：目录原示例仅含 province/city/county（筛选项片段，缺主体），
		// 2026-09-17 起示例改取自原始文档（{"entName":"...","pageSize":1}），已可照抄。
		e := c.GetByPcode("E_007_0701")
		if e == nil {
			t.Fatal("未找到 E_007_0701")
		}
		if !e.ExampleFromDoc {
			t.Fatal("示例应取自原始文档")
		}
		if _, ok := e.ExampleRequest["entName"]; !ok {
			t.Fatalf("文档示例应含主体参数 entName: %v", e.ExampleRequest)
		}
		_, errs, warns := Validate(e, map[string]any{})
		if len(errs) == 0 {
			t.Fatal("空参仍应报缺必填")
		}
		for _, w := range warns {
			if strings.Contains(w, "勿直接照抄") {
				t.Fatalf("示例已含主体参数，不应再提示照抄风险: %v", warns)
			}
		}
	})
}
