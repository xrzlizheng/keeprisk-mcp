package mcp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

// 历史编码与未知编码的本地拒绝语义（2026-09-22 定标：映射已删除，二者同等待遇）：
//   - 不自动升级、不发网关、不计费；
//   - 文案统一"未知接口编码"，不回显具体编码（对模型不可见）、不给替代提示；
//   - 三个入口（keeprisk_query / get_keeprisk_api / validate_keeprisk_params）行为一致。
func TestHistoricalPcodeRejectedAsUnknown(t *testing.T) {
	cs := startTestServer(t)
	gwHits.Store(0)

	// 历史编码（含原 7 组映射的全部旧码）：一律按未知编码本地拒绝
	historical := []string{"E_011_0261", "E_011_0271", "E_007_0921", "E_007_1281", "E_007_3901", "E_017_0001", "E_017_0071"}
	for _, old := range historical {
		out := callToolRaw(t, cs, "keeprisk_query", map[string]any{
			"pcode": old, "params": map[string]any{},
		})
		if !strings.Contains(out, "未知接口编码") {
			t.Fatalf("%s 应按未知编码本地拒绝，got: %s", old, out)
		}
		if strings.Contains(out, "已下线") {
			t.Fatalf("%s 不应再给「已下线/替代」提示（映射已删除）: %s", old, out)
		}
		if strings.Contains(out, old) {
			t.Fatalf("%s 拒绝文案回显了历史编码（模型可见面禁止出现编码）: %s", old, out)
		}
	}
	// get_keeprisk_api / validate_keeprisk_params 同语义
	out := callToolRaw(t, cs, "get_keeprisk_api", map[string]any{"pcode": "E_011_0261"})
	if !strings.Contains(out, "未知接口编码") || strings.Contains(out, "E_011_0261") {
		t.Fatalf("get_keeprisk_api 对历史编码应按未知拒绝且不回显: %s", out)
	}
	out = callToolRaw(t, cs, "validate_keeprisk_params", map[string]any{"pcode": "E_011_0271", "params": map[string]any{}})
	if !strings.Contains(out, "未知接口编码") || strings.Contains(out, "E_011_0271") {
		t.Fatalf("validate_keeprisk_params 对历史编码应按未知拒绝且不回显: %s", out)
	}
	// 未知编码
	out = callToolRaw(t, cs, "keeprisk_query", map[string]any{"pcode": "E_999_9999", "params": map[string]any{}})
	if !strings.Contains(out, "未知接口编码") || strings.Contains(out, "E_999_9999") {
		t.Fatalf("未知编码应本地拒绝且不回显: %s", out)
	}

	if got := gwHits.Load(); got != 0 {
		t.Fatalf("本地拒绝仍触达网关 %d 次（应为 0，不计费）", got)
	}
}

// AI 可见文本守卫：instructions 与全部工具描述中出现的接口编码，
// 必须都是**当前有效**接口（历史/未知编码一律不得出现）。
// 现阶段可见文本应为零编码——本测试同时防止未来回潮。
func TestNoStalePcodesInModelFacingText(t *testing.T) {
	pat := regexp.MustCompile(`[EP]_\d{3}_\d{4}`)
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	check := func(where, text string) {
		for _, code := range pat.FindAllString(text, -1) {
			if cat.GetByPcode(code) == nil {
				t.Fatalf("%s 出现非当前有效编码 %s（历史/未知编码不得对模型可见）", where, code)
			}
		}
	}
	check("instructions", ServerInstructions())

	cs := startTestServer(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("序列化工具清单: %v", err)
	}
	check("tools/list", string(blob))
	if len(res.Tools) == 0 {
		t.Fatal("tools/list 为空")
	}
}
