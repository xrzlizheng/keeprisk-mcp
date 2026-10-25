package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// 回归：total_hits 必须读 data.total（生产网关实测响应结构），而非顶层 total。
func TestResolveEntityReadsNestedTotal(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		if pcode != "E_007_1331" {
			t.Errorf("unexpected pcode %q", pcode)
		}
		return mustJSON(map[string]any{
			"code":       100,
			"pcode_used": "E_007_1331",
			"data": map[string]any{
				"total": 57,
				"list": []any{
					map[string]any{"entName": "小米科技有限责任公司", "creditCode": "91110108551385082Q"},
					map[string]any{"entName": "广东小米科技有限责任公司"},
				},
			},
		})
	}
	raw := svc.ResolveEntity(context.Background(), "小米科技", "", 5)
	parsed := mustParseJSON(t, raw)
	if got := int(parsed["total_hits"].(float64)); got != 57 {
		t.Fatalf("total_hits = %d, want 57（应读 data.total 嵌套层级）; raw=%s", got, raw)
	}
	if got := int(parsed["candidates_shown"].(float64)); got != 2 {
		t.Fatalf("candidates_shown = %d, want 2; raw=%s", got, raw)
	}
}

// 回归：QueryImpl 附加的网关层元数据（_cache_hit/pcode_used/warnings）必须透传到输出，
// 否则调用方无法判断本次响应是否命中缓存（是否计费）。
func TestResolveEntityPropagatesQueryMeta(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		return mustJSON(map[string]any{
			"code":       100,
			"pcode_used": "E_007_1331",
			"_cache_hit": true,
			"warnings":   []any{"未知参数 foo"},
			"data": map[string]any{
				"total": 1,
				"list":  []any{map[string]any{"entName": "A公司"}},
			},
		})
	}
	raw := svc.ResolveEntity(context.Background(), "A公司", "", 5)
	parsed := mustParseJSON(t, raw)
	if v, ok := parsed["_cache_hit"].(bool); !ok || !v {
		t.Fatalf("_cache_hit = %v, want true（应从底层响应透传）; raw=%s", parsed["_cache_hit"], raw)
	}
	if v, _ := parsed["pcode_used"].(string); v != "E_007_1331" {
		t.Fatalf("pcode_used = %v, want E_007_1331; raw=%s", parsed["pcode_used"], raw)
	}
	if ws, ok := parsed["warnings"].([]any); !ok || len(ws) != 1 {
		t.Fatalf("warnings = %v, want 1 条透传; raw=%s", parsed["warnings"], raw)
	}
}

// 回归：data.total 与顶层 total 都缺失时，total_hits 兜底为候选条数，不再虚构 0。
func TestResolveEntityTotalFallsBackToCandidates(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		return mustJSON(map[string]any{
			"code": 100,
			"data": map[string]any{
				"list": []any{
					map[string]any{"entName": "A公司"},
					map[string]any{"entName": "B公司"},
				},
			},
		})
	}
	raw := svc.ResolveEntity(context.Background(), "公司", "", 5)
	parsed := mustParseJSON(t, raw)
	if got := int(parsed["total_hits"].(float64)); got != 2 {
		t.Fatalf("total_hits = %d, want 2（兜底为候选数，不应为 0）; raw=%s", got, raw)
	}
}

// keyword 缺失/空白 → 本地 validation 错误，不调网关。
func TestResolveEntityRequiresKeyword(t *testing.T) {
	svc := &Services{}
	called := false
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		called = true
		return "{}"
	}
	raw := svc.ResolveEntity(context.Background(), "   ", "", 5)
	if called {
		t.Fatalf("空 keyword 不应触发网关调用")
	}
	parsed := mustParseJSON(t, raw)
	if _, ok := parsed["error"]; !ok {
		t.Fatalf("expected error in output, got %s", raw)
	}
}

// region 非空时必须透传为 E_007_1331 的 region 参数。
func TestResolveEntityPassesRegion(t *testing.T) {
	svc := &Services{}
	var gotParams map[string]any
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		gotParams = params
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"total": 0, "list": []any{}}})
	}
	_ = svc.ResolveEntity(context.Background(), "小米", "北京", 5)
	if gotParams == nil {
		t.Fatalf("gateway not called")
	}
	region, ok := gotParams["region"].([]map[string]string)
	if !ok || len(region) != 1 || region[0]["pr"] != "北京" {
		t.Fatalf("region param = %v, want [{pr:北京}]", gotParams["region"])
	}
}

func mustParseJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw=%s", err, raw)
	}
	return m
}
