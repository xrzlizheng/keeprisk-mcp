package mcp

import (
	"context"
	"sync"
	"testing"
)

// ─────────────── P0-2 market 全市场节回归 ───────────────

// listed_company_market 不得调用全市场列表接口（E_010_0001/E_010_1301），
// 也不得把企业名误传给它们的主体字段。
func TestListedCompanyMarketSkipsMarketWideSections(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	var called []string
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		// 聚合工具会并发调用本闭包，共享 slice 必须加锁（-race 检测到的数据竞争）
		mu.Lock()
		called = append(called, pcode)
		mu.Unlock()
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}, "total": 0}})
	}
	raw := svc.ListedCompanyMarket(context.Background(), "贵州茅台酒股份有限公司", "")
	mu.Lock()
	snapshot := append([]string(nil), called...)
	mu.Unlock()
	for _, pc := range snapshot {
		if pc == "E_010_0001" || pc == "E_010_1301" {
			t.Fatalf("全市场接口 %s 不应被 listed_company_market 调用；called=%v", pc, called)
		}
	}
	parsed := mustParseJSON(t, raw)
	if got := int(parsed["total_count"].(float64)); got != 15 {
		t.Fatalf("total_count = %d, want 15（移除 2 个全市场节后）; raw=%s", got, raw)
	}
	if _, ok := parsed["_agent_meta"]; !ok {
		t.Fatalf("聚合输出缺少 _agent_meta; raw=%s", raw)
	}
}

// ─────────────── P1 supply_chain 节点统计回归 ───────────────

// industry_chain_nodes（E_007_1251）主体是 chainName，企业维度必须跳过，
// 不得把企业名误传给 name 字段打到网关。
func TestSupplyChainRelationsSkipsChainNodes(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	var called []string
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		// 聚合工具会并发调用本闭包，共享 slice 必须加锁（-race 检测到的数据竞争）
		mu.Lock()
		called = append(called, pcode)
		mu.Unlock()
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}, "total": 0}})
	}
	raw := svc.SupplyChainRelations(context.Background(), "测试企业", "")
	mu.Lock()
	snapshot := append([]string(nil), called...)
	mu.Unlock()
	for _, pc := range snapshot {
		if pc == "E_007_1251" {
			t.Fatalf("E_007_1251 不应以企业名调用；called=%v", called)
		}
	}
	parsed := mustParseJSON(t, raw)
	if got := int(parsed["skipped_count"].(float64)); got != 1 {
		t.Fatalf("skipped_count = %d, want 1（chain nodes 节应 skip）; raw=%s", got, raw)
	}
}

// ─────────────── P0-3 尽调 digest 空主体回归 ───────────────

// basic 节无企业字段时：has_records=false、risk_summary 出现 warning，
// 而不是「有记录+绿灯」幻觉。
func TestDueDiligenceBasicNoDataNotGreen(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		// code=110 成功但无记录（企业不存在）
		return mustJSON(map[string]any{"code": 110, "message": "查询成功，无记录"})
	}
	raw := svc.DueDiligenceSnapshot(context.Background(), "一个肯定不存在的企业名称测试用例", "", "basic", false, 2, false)
	parsed := mustParseJSON(t, raw)
	sections := parsed["sections"].([]any)
	var basic map[string]any
	for _, s := range sections {
		if m, ok := s.(map[string]any); ok && m["key"] == "basic" {
			basic = m
		}
	}
	if basic == nil {
		t.Fatalf("missing basic section; raw=%s", raw)
	}
	dgst := basic["digest"].(map[string]any)
	if hasRecords, _ := dgst["has_records"].(bool); hasRecords {
		t.Fatalf("basic 无企业字段时 has_records 必须为 false; digest=%v", dgst)
	}
	rs := parsed["risk_summary"].(map[string]any)
	if _, ok := rs["warning"]; !ok {
		t.Fatalf("basic 无记录时 risk_summary 必须包含 warning; risk_summary=%v", rs)
	}
}

// 有企业字段时保持原行为：has_records=true 且摘要含企业名。
func TestDueDiligenceBasicWithData(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		return mustJSON(map[string]any{
			"code": 100,
			"data": map[string]any{"entName": "A公司", "legalName": "张三", "entStatus": "在业"},
		})
	}
	raw := svc.DueDiligenceSnapshot(context.Background(), "A公司", "", "basic", false, 2, false)
	parsed := mustParseJSON(t, raw)
	sections := parsed["sections"].([]any)
	basic := sections[0].(map[string]any)
	dgst := basic["digest"].(map[string]any)
	if hasRecords, _ := dgst["has_records"].(bool); !hasRecords {
		t.Fatalf("有企业字段时 has_records 应为 true; digest=%v", dgst)
	}
}

// ─────────────── P1 judicial_deep_dive company_id 回归 ───────────────

// company_id-only 调用：先反查企业名，企业名只进列表接口；
// 明细接口必须收列表记录 id，绝不能再收企业名（旧实现 6 节 101 参数错误的根因）。
func TestJudicialDeepDiveResolvesCompanyID(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	listEntNames := 0
	detailWithEntName := 0
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		if pcode == "E_007_0501" {
			return mustJSON(map[string]any{
				"code": 100,
				"data": map[string]any{"entName": "贵州茅台酒股份有限公司"},
			})
		}
		mu.Lock()
		if _, hasID := params["id"]; hasID {
			if _, hasName := params["entName"]; hasName {
				detailWithEntName++
			}
		} else if name, ok := params["entName"].(string); ok && name != "" {
			listEntNames++
		}
		mu.Unlock()
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}, "total": 0}})
	}
	raw := svc.JudicialDeepDive(context.Background(), "", "cid_123")
	parsed := mustParseJSON(t, raw)
	mu.Lock()
	names, bad := listEntNames, detailWithEntName
	mu.Unlock()
	if names == 0 {
		t.Fatalf("company_id-only 调用应先反查 entName 并传给列表接口; raw=%s", raw)
	}
	if bad > 0 {
		t.Fatalf("明细接口不得携带 entName（须用列表记录 id）; raw=%s", raw)
	}
	if _, ok := parsed["ent_name_resolved"]; !ok {
		t.Fatalf("应输出 ent_name_resolved 标记; raw=%s", raw)
	}
	if got := int(parsed["api_calls"].(float64)); got != 3 {
		t.Fatalf("api_calls = %d, want 3（反查+司法协助列表+破产列表）; raw=%s", got, raw)
	}
}

// ─────────────── P1 聚合输出反幻觉元数据回归 ───────────────

func TestBuildPhase6OutputHasAgentMeta(t *testing.T) {
	out := mustParseJSON(t, buildPhase6Output("t", "企业X", nil, 0, 0, 0, 0))
	if _, ok := out["_agent_meta"]; !ok {
		t.Fatal("buildPhase6Output 输出必须包含 _agent_meta")
	}
}

// ─────────────── helpers ───────────────
// mustParseJSON 复用 resolve_entity_test.go 中的同名 helper。
