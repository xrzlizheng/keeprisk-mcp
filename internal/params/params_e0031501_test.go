package params

import (
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// 回归（P1）：E_003_1501 司法拍卖 catalog 未标 required_one_of，
// 空参曾穿透本地校验打到网关返回 101。代码级兜底必须拦截。
func TestValidateE0031501EmptyParamsRejected(t *testing.T) {
	entry := &catalog.APIEntry{Pcode: "E_003_1501", Title: "司法拍卖"}
	_, errs, _ := Validate(entry, map[string]any{})
	if len(errs) == 0 {
		t.Fatal("E_003_1501 空参应在本地校验被拒绝（requiredOneOfOverrides 兜底）")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "至少填一个") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误信息应提示「至少填一个」；errs=%v", errs)
	}
}

func TestValidateE0031501WithEntNamePasses(t *testing.T) {
	entry := &catalog.APIEntry{Pcode: "E_003_1501", Title: "司法拍卖"}
	_, errs, _ := Validate(entry, map[string]any{"ent_name": "A公司"})
	if len(errs) != 0 {
		t.Fatalf("提供 ent_name 后不应有校验错误；errs=%v", errs)
	}
}
