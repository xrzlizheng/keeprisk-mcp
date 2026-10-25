package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// ─────────────── judicial_deep_dive 列表→明细链路回归 ───────────────
// 背景（2026-09-17 线上故障）：司法明细接口（E_007_2401~2801/E_003_1821）唯一入参是
// 【列表记录 id】，旧实现直接把企业名塞给明细接口，网关 6 节全部返回 101 参数错误，
// 且汇总层把 error 静默吞掉输出 total_judicial_records=0 / overall_risk=low 的假绿灯。
//
// 实测网关契约：
//   E_007_2301(entName) → {list:[{id,typeState:冻结|解除冻结|…}]}
//   E_007_2401~2801({id}) → 该记录明细（id 打错类型接口返回 110，路由必须按 typeState）
//   E_003_1811(name) → {list:[{id}]} → E_003_1821({id})

type gwCall struct {
	pcode  string
	params map[string]any
}

// stubJudicialGateway 返回可编程桩：按 pcode 命中响应表，未命中返回 110。
func stubJudicialGateway(t *testing.T, resp map[string]string, calls *[]gwCall, mu *sync.Mutex) func(context.Context, string, map[string]any) string {
	t.Helper()
	return func(_ context.Context, pcode string, params map[string]any) string {
		mu.Lock()
		*calls = append(*calls, gwCall{pcode: pcode, params: params})
		mu.Unlock()
		if raw, ok := resp[pcode]; ok {
			return raw
		}
		return mustJSON(map[string]any{"code": "110", "message": "查询成功，无数据", "data": nil})
	}
}

func sectionByKey(t *testing.T, parsed map[string]any, key string) map[string]any {
	t.Helper()
	sections, _ := parsed["sections"].([]any)
	for _, s := range sections {
		if m, ok := s.(map[string]any); ok && m["key"] == key {
			return m
		}
	}
	t.Fatalf("缺少 section %s; sections=%v", key, parsed["sections"])
	return nil
}

// 完整链路：company_id 反查企业名 → 司法协助列表 → 按 typeState 路由明细 → 破产列表 → 案件明细。
func TestJudicialDeepDiveChainRoutesDetailsByID(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	var calls []gwCall
	svc.QueryFn = stubJudicialGateway(t, map[string]string{
		"E_007_0501": mustJSON(map[string]any{"code": 100, "data": map[string]any{"entName": "贵州茅台酒股份有限公司"}}),
		"E_007_2301": mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 2, "list": []any{
			map[string]any{"id": "id-freeze", "typeState": "冻结", "executiveCourt": "沈阳市中级人民法院", "equityAmount": "3787万人民币"},
			map[string]any{"id": "id-unfreeze", "typeState": "解除冻结", "executiveCourt": "某法院"},
		}}}),
		"E_007_2401": mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 1, "list": []any{
			map[string]any{"id": "id-freeze", "period": "1095", "fromDate": "2023-11-29"},
		}}}),
		"E_007_2701": mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 1, "list": []any{
			map[string]any{"id": "id-unfreeze", "toDate": "2026-01-01"},
		}}}),
		"E_003_1811": mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 1, "list": []any{
			map[string]any{"id": "id-bankrupt", "caseNo": "(2020)破1号"},
		}}}),
		"E_003_1821": mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 1, "list": []any{
			map[string]any{"id": "id-bankrupt", "status": "审理中"},
		}}}),
	}, &calls, &mu)

	raw := svc.JudicialDeepDive(context.Background(), "", "cid_123")
	parsed := mustParseJSON(t, raw)

	mu.Lock()
	snapshot := append([]gwCall(nil), calls...)
	mu.Unlock()

	// 明细接口必须收到记录 id，且绝不能收到企业名
	wantID := map[string]string{"E_007_2401": "id-freeze", "E_007_2701": "id-unfreeze", "E_003_1821": "id-bankrupt"}
	for pcode, id := range wantID {
		found := false
		for _, c := range snapshot {
			if c.pcode != pcode {
				continue
			}
			found = true
			if got, _ := c.params["id"].(string); got != id {
				t.Fatalf("%s 应收到 id=%s, got %q; params=%v", pcode, id, got, c.params)
			}
			if _, has := c.params["entName"]; has {
				t.Fatalf("%s 明细调用不得携带 entName; params=%v", pcode, c.params)
			}
		}
		if !found {
			t.Fatalf("未调用明细接口 %s; called=%+v", pcode, snapshot)
		}
	}
	// 列表接口收到反查出的企业名
	for _, c := range snapshot {
		if c.pcode == "E_007_2301" {
			if got, _ := c.params["entName"].(string); got != "贵州茅台酒股份有限公司" {
				t.Fatalf("列表接口应收到反查 entName, got %q", got)
			}
		}
	}

	if _, ok := parsed["ent_name_resolved"]; !ok {
		t.Fatalf("应输出 ent_name_resolved; raw=%s", raw)
	}
	if got := int(parsed["api_calls"].(float64)); got != 6 { // 0501+2301+2401+2701+1811+1821
		t.Fatalf("api_calls = %d, want 6; raw=%s", got, raw)
	}
	if got := int(parsed["total_judicial_records"].(float64)); got != 3 {
		t.Fatalf("total_judicial_records = %d, want 3; raw=%s", got, raw)
	}
	if parsed["overall_risk"] != "high" {
		t.Fatalf("有冻结记录时 overall_risk 应为 high; raw=%s", raw)
	}
	for key, wantStatus := range map[string]string{
		"judicial_assistance":         "ok",
		"judicial_freeze":             "ok",
		"judicial_unfreeze":           "ok",
		"judicial_freeze_expiry":      "no_data",
		"judicial_continuation":       "no_data",
		"judicial_shareholder_change": "no_data",
		"bankruptcy_detail":           "ok",
	} {
		if got := sectionByKey(t, parsed, key)["status"]; got != wantStatus {
			t.Fatalf("section %s status = %v, want %s", key, got, wantStatus)
		}
	}
}

// 无记录企业（安徽智侒信线上案例）：必须输出绿色结论，不得出现 error 节。
func TestJudicialDeepDiveNoRecordsGreenLight(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	var calls []gwCall
	svc.QueryFn = stubJudicialGateway(t, nil, &calls, &mu) // 全部 110

	raw := svc.JudicialDeepDive(context.Background(), "安徽智侒信信息技术有限公司", "")
	parsed := mustParseJSON(t, raw)
	if parsed["overall_risk"] != "low" {
		t.Fatalf("无记录应 overall_risk=low; raw=%s", raw)
	}
	if got := int(parsed["total_judicial_records"].(float64)); got != 0 {
		t.Fatalf("total_judicial_records = %d, want 0", got)
	}
	if got := int(parsed["error_count"].(float64)); got != 0 {
		t.Fatalf("error_count = %d, want 0（110 是成功无记录，不是错误）", got)
	}
	sections, _ := parsed["sections"].([]any)
	if len(sections) == 0 {
		t.Fatalf("sections 为空; raw=%s", raw)
	}
	for _, s := range sections {
		m := s.(map[string]any)
		if m["status"] != "no_data" {
			t.Fatalf("section %v status=%v, want no_data", m["key"], m["status"])
		}
	}
}

// 网关故障：禁止假绿灯——overall_risk=unknown，错误 message 透出到 section。
func TestJudicialDeepDiveGatewayErrorNotGreenRisk(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(context.Context, string, map[string]any) string {
		// 复刻旧故障形态：QueryImpl 对网关 101 的包装
		return mustJSON(map[string]any{"error": "api", "code": "101", "message": "code=101 参数错误（参数不符合接口要求）", "data": nil})
	}
	raw := svc.JudicialDeepDive(context.Background(), "安徽智侒信信息技术有限公司", "")
	parsed := mustParseJSON(t, raw)
	if parsed["overall_risk"] != "unknown" {
		t.Fatalf("网关故障时 overall_risk 应为 unknown（禁止假绿灯）; raw=%s", raw)
	}
	if got := int(parsed["error_count"].(float64)); got < 1 {
		t.Fatalf("error_count = %d, want >=1", got)
	}
	sec := sectionByKey(t, parsed, "judicial_assistance")
	if msg, _ := sec["error"].(string); !strings.Contains(msg, "参数错误") {
		t.Fatalf("section error 应透出网关 message, got %q", msg)
	}
}

// typeState 路由表：顺序敏感（解除冻结/冻结失效含"冻结"子串，必须先匹配特例）。
func TestJudicialDetailRoute(t *testing.T) {
	cases := []struct {
		typeState string
		pcode     string
		ok        bool
	}{
		{"冻结", "E_007_2401", true},
		{"冻结失效", "E_007_2501", true},
		{"失效", "E_007_2501", true},
		{"解除冻结", "E_007_2701", true},
		{"续行", "E_007_2601", true},
		{"续冻", "E_007_2601", true},
		{"股东变更", "E_007_2801", true},
		{"股权变更", "E_007_2801", true},
		{"股权冻结", "E_007_2401", true},
		{"", "", false},
		{"其他未知类型", "", false},
	}
	for _, c := range cases {
		pcode, _, ok := judicialDetailRoute(c.typeState)
		if pcode != c.pcode || ok != c.ok {
			t.Fatalf("judicialDetailRoute(%q) = (%q,%v), want (%q,%v)", c.typeState, pcode, ok, c.pcode, c.ok)
		}
	}
}
