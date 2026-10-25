package params

import (
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// 司法/破产详情 6 接口以【列表记录 id】为唯一必填主体（PDF：id 必填）。
// 企业名误作 id 曾致网关 101 参数错误 6 节全灭（2026-09-17 线上实测），
// 本地校验必须阻断缺 id 的调用并提示企业名是未知参数。
// 用真实目录加载，防止 requiredFields 与 catalog 漂移。
func TestJudicialDetailAPIsRequireRecordID(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, pcode := range []string{"E_003_1821", "E_007_2401", "E_007_2501", "E_007_2601", "E_007_2701", "E_007_2801"} {
		entry := cat.GetByPcode(pcode)
		if entry == nil {
			t.Fatalf("%s 不在目录中", pcode)
		}
		_, errs, warns := Validate(entry, map[string]any{"entName": "安徽智侒信信息技术有限公司"})
		hasIDErr := false
		for _, e := range errs {
			if strings.Contains(e, "id") {
				hasIDErr = true
			}
		}
		if !hasIDErr {
			t.Fatalf("%s 缺 id 应被本地阻断; errs=%v", pcode, errs)
		}
		hasNameWarn := false
		for _, w := range warns {
			if strings.Contains(w, "entName") {
				hasNameWarn = true
			}
		}
		if !hasNameWarn {
			t.Fatalf("%s 应警告 entName 是未知参数; warns=%v", pcode, warns)
		}

		_, errs, _ = Validate(entry, map[string]any{"id": "b93b8599a3e2f355c7b7d2569d218222"})
		if len(errs) != 0 {
			t.Fatalf("%s 只传 id 应通过本地校验; errs=%v", pcode, errs)
		}
	}
}
