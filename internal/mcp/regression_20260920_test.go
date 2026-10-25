package mcp

// 2026-09-20 全量实测修复的回归测试（主体：安徽智侒信信息技术有限公司）。
//
// 本文件只放 fix_regress_20260920_test.go 未覆盖的三项：
//  1. playbook「司法排查」必须含全部 12 司法节（P0-A：本主体唯一红级来源
//     court_announcement 曾恰好不在推荐子集里）+ 文档残留/样本污染；
//  2. 缓存 key 分页归一的端到端行为（P1-C：同接口不同 pageSize 曾重复计费）；
//  3. instructions 瘦身后仍含关键护栏且体积受控（P2-A：每会话固定成本）。

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

func TestPlaybookJudicialSectionsComplete(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	out := (&Services{Cat: cat}).ListCapabilities()
	for _, must := range []string{"court_announcement", "serious_dishonesty", "judicial_auction"} {
		if !strings.Contains(out, must) {
			t.Fatalf("agent_playbook 司法排查缺少 %s——按该剧本执行的 Agent 会漏报红级来源", must)
		}
	}
	if strings.Contains(out, "read_keeprisk_api_doc") {
		t.Fatal("note 仍引用不存在的 Python stdio 工具 read_keeprisk_api_doc（会诱导调用不存在的工具）")
	}
	for _, polluted := range []string{"国元", "大衍", "天仪", "宣城", "德勤", "德拓", "黄山"} {
		if strings.Contains(out, polluted) {
			t.Fatalf("公共分类样本仍混入定制产品线分类 %q", polluted)
		}
	}
}

// TestCachePageSizeNormalizationE2E 端到端锁定缓存 key 分页归一：
// 同主体同接口、不同 pageSize 的两次调用，第二次必须复用缓存（0 网关调用）。
// 这正是实测 E_007_0601 被 5/10/20 三种 pageSize 打散、一回合重复计费 3 次的场景。
func TestCachePageSizeNormalizationE2E(t *testing.T) {
	cs := startTestServer(t)
	before := gwHits.Load()

	callTool(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "E_007_0601",
		"params": map[string]any{"entName": "分页归一测试公司", "pageNo": 1, "pageSize": 20},
	})
	if n := gwHits.Load() - before; n != 1 {
		t.Fatalf("首次调用应 1 次网关，实为 %d", n)
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "keeprisk_query", Arguments: map[string]any{
		"pcode":  "E_007_0601",
		"params": map[string]any{"entName": "分页归一测试公司", "pageNo": 1, "pageSize": 5},
	}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if n := gwHits.Load() - before; n != 1 {
		t.Fatalf("pageSize=5 应复用 20 窗口缓存（0 网关调用），实多打了 %d 次", n-1)
	}
	if text := toolResultText(res); !strings.Contains(text, `"_cache_hit":true`) {
		t.Fatalf("第二次调用应带 _cache_hit:true: %s", text[:200])
	}
}

func TestServerInstructionsSlimButGuarded(t *testing.T) {
	s := ServerInstructions()
	if n := len(s); n > 3200 {
		t.Fatalf("instructions 应 ≤3200 字符（每会话固定成本），现 %d", n)
	}
	for _, must := range []string{"无数据", "code=110", "Authorization", "actual_controller"} {
		if !strings.Contains(s, must) {
			t.Fatalf("instructions 瘦身后丢失关键护栏 %q", must)
		}
	}
}

// P0-C：十大股东 digest 必须解析真实载荷形态（科大讯飞 2026-09-20 线上实测）：
// envelope.data = {data:{bq,resultdate}, msg, status}，bq 在 data 变量下一跳 .data 处。
// 曾有两版 bug：extractCount 认不出（恒 0）；以及多跳一层 data 导致线上 digest 仍 0。
func TestTop10ShareholdersDigestRealShape(t *testing.T) {
	parsed := map[string]any{
		"charge": true, "code": "100",
		"data": map[string]any{
			"data": map[string]any{
				"bq": []any{
					map[string]any{
						"enddate": "2025-12-31 00:00:00",
						"tc":      []any{map[string]any{"SHLIST": "C"}},
						"zc":      []any{map[string]any{"SHLIST": "A"}, map[string]any{"SHLIST": "B"}},
					},
				},
				"resultdate": []any{map[string]any{"ENDDATE": "2025-12-31"}},
			},
			"msg": "查询成功", "status": 200.0,
		},
		"message": "查询成功",
	}
	dg := buildListedDigest("top10_shareholders", parsed)
	if dg["shareholder_count"] != 3 {
		t.Fatalf("shareholder_count 应为 3（zc2+tc1），实际 %v", dg["shareholder_count"])
	}
	if dg["period_count"] != 1 {
		t.Fatalf("period_count 应为 1，实际 %v", dg["period_count"])
	}
	if dg["has_data"] != true {
		t.Fatalf("has_data 应为 true，实际 %v（载荷 1 期 3 股东）", dg["has_data"])
	}
	if dg["latest_period"] != "2025-12-31 00:00:00" {
		t.Fatalf("latest_period=%v", dg["latest_period"])
	}
}

// P0-C：top10 两节默认 number="1"（最新报告期）。number="0" 返回全部报告期
// （实测科大讯飞 77 期 342KB ≈ 13 万 token，单次调用可撑爆上下文）。
func TestTop10SectionsRequestLatestPeriodOnly(t *testing.T) {
	for _, sec := range listedSections {
		if sec.Key != "top10_shareholders" && sec.Key != "top10_circulating" {
			continue
		}
		if sec.ExtraParams["number"] != "1" {
			t.Fatalf("%s 的 number=%v，应为 \"1\"", sec.Key, sec.ExtraParams["number"])
		}
	}
}

// P1：willingness 两节未提供 chain_name 时必须 skipped（0 网关）。
// E_017_0161/0231 为产业链维度接口（文档必填 chainName），旧实现只传企业名，
// 对所有主体恒 101 白付调用。
func TestInvestmentReadinessSkipsWillingnessWithoutChain(t *testing.T) {
	var called []string
	svc := &Services{QueryFn: func(ctx context.Context, pcode string, params map[string]any) string {
		called = append(called, pcode)
		return `{"code":"100","data":{"entEntireScore":85.2}}`
	}}
	out := svc.InvestmentReadinessScore(context.Background(), "安徽智侒信信息技术有限公司", "", "", "", "")
	if !strings.Contains(out, `"status":"skipped"`) {
		t.Fatalf("无 chain_name 时 willingness 两节应 skipped: %s", out[:300])
	}
	for _, p := range called {
		if p == "E_017_0161" || p == "E_017_0231" {
			t.Fatalf("无 chain_name 不应触达 %s", p)
		}
	}
}

// TestCacheWrapperFallthroughNotLeaked 窗口不足时的穿透路径不得把缓存包装体当响应返回。
// 2026-09-20 实测：judicial_deep_dive 请求 pageSize=20 而缓存里是 snapshot 存的 sz=5 条目，
// 旧回退分支把 {"sz":5,"resp":"..."} 包装体当旧格式响应返回，judicial 解析失败报"网关故障"。
func TestCacheWrapperFallthroughNotLeaked(t *testing.T) {
	cs := startTestServer(t)
	before := gwHits.Load()

	// 小窗口先入缓存（模拟 snapshot 注入 pageSize=5）
	callTool(t, cs, "keeprisk_query", map[string]any{
		"pcode":  "E_007_0601",
		"params": map[string]any{"entName": "穿透测试公司", "pageNo": 1, "pageSize": 3},
	})
	if n := gwHits.Load() - before; n != 1 {
		t.Fatalf("首次应 1 次网关，实为 %d", n)
	}
	// 大窗口请求：sz=3 < 20 → 必须穿透真实调用，且响应不得是包装体
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "keeprisk_query", Arguments: map[string]any{
		"pcode":  "E_007_0601",
		"params": map[string]any{"entName": "穿透测试公司", "pageNo": 1, "pageSize": 20},
	}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if n := gwHits.Load() - before; n != 2 {
		t.Fatalf("窗口不足应穿透真实调用（共 2 次网关），实为 %d", n)
	}
	if text := toolResultText(res); strings.Contains(text, `"sz"`) || strings.Contains(text, `"resp"`) {
		t.Fatalf("穿透路径把缓存包装体泄漏给了调用方: %s", text[:200])
	}
}

// toolResultText 提取 CallTool 结果文本（本文件内部用）。
func toolResultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
