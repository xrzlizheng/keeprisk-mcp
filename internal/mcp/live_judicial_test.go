package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
)

// 真实网关端到端回归（计费调用，默认跳过）：
//
//	set -a && source .env && set +a
//	KEEPRISK_LIVE_JUDICIAL=1 go test -run TestLiveJudicialDeepDiveChain ./internal/mcp/ -v
//
// 必须用 -run 单独执行：本包 TestMain 会把 KEEPRISK_API_BASE 指到本地 httptest 假网关，
// 本测试在其之后显式覆盖回真实网关（DefaultClient 惰性初始化，首次调用才绑定地址）。
//
// 计费授权：网关调用只允许使用 KEEPRISK_LIVE_TOKEN 指定的那一组授权映射
// （由管理员发放，切勿写入仓库）；其余账号不得用于测试。
//
// 契约（2026-09-17/18 网关实测）：
//   - 无司法记录企业（安徽智侒信）：各节 no_data、overall_risk=low、零 error 节；
//   - 有司法协助记录企业（恒大地产）：列表给记录 id+typeState=冻结×3，
//     冻结节按 id 路由明细 ok，破产节 no_data。
func TestLiveJudicialDeepDiveChain(t *testing.T) {
	if os.Getenv("KEEPRISK_LIVE_JUDICIAL") != "1" {
		t.Skip("真实网关计费回归；需 KEEPRISK_LIVE_JUDICIAL=1 + KEEPRISK_AUTH_MAP")
	}
	liveAuthorizedToken := os.Getenv("KEEPRISK_LIVE_TOKEN")
	if liveAuthorizedToken == "" {
		t.Skip("真实网关计费回归；需 KEEPRISK_LIVE_TOKEN（管理员发放，勿入库）+ KEEPRISK_AUTH_MAP")
	}
	const liveGatewayBase = "https://api.keeprisk.com:9080"
	// 必须在 MustServices（惰性创建网关客户端）之前覆盖包级 TestMain 注入的假网关地址
	t.Setenv("KEEPRISK_API_BASE", liveGatewayBase)
	svc, err := MustServices()
	if err != nil {
		t.Fatalf("MustServices: %v", err)
	}
	authMap := map[string]string{}
	if raw := os.Getenv("KEEPRISK_AUTH_MAP"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &authMap)
	}
	appcode := authMap[liveAuthorizedToken]
	if appcode == "" {
		tokenHint := liveAuthorizedToken
		if len(tokenHint) > 16 {
			tokenHint = tokenHint[:16] + "…"
		}
		t.Skip("KEEPRISK_AUTH_MAP 中无 KEEPRISK_LIVE_TOKEN 对应的授权 " + tokenHint)
	}
	ctx := auth.WithAppCode(context.Background(), appcode)

	// 案例 1：无司法记录企业（本次故障的原始主体）
	raw := svc.JudicialDeepDive(ctx, "安徽智侒信信息技术有限公司", "687f2c11206abbacaa0b467a18e40666")
	parsed := mustParseJSON(t, raw)
	if parsed["overall_risk"] != "low" {
		t.Fatalf("无记录企业应 overall_risk=low; raw=%s", raw)
	}
	if got := int(parsed["error_count"].(float64)); got != 0 {
		t.Fatalf("无记录企业不应有 error 节; raw=%s", raw)
	}
	t.Logf("案例1（无记录）: %s", summarizeJudicial(parsed))

	// 案例 2：有司法协助记录企业（实测：恒大地产 冻结×3，破产无记录）
	raw2 := svc.JudicialDeepDive(ctx, "恒大地产集团有限公司", "")
	parsed2 := mustParseJSON(t, raw2)
	if got := int(parsed2["total_judicial_records"].(float64)); got != 3 {
		t.Fatalf("恒大地产司法协助应为 3 条; raw=%s", raw2)
	}
	if parsed2["overall_risk"] != "high" {
		t.Fatalf("有冻结记录应 overall_risk=high; raw=%s", raw2)
	}
	assist := sectionByKey(t, parsed2, "judicial_assistance")
	if assist["status"] != "ok" {
		t.Fatalf("司法协助列表节应 ok; assist=%v", assist)
	}
	freeze := sectionByKey(t, parsed2, "judicial_freeze")
	if freeze["status"] != "ok" {
		t.Fatalf("冻结节应 ok; freeze=%v", freeze)
	}
	details, _ := freeze["details"].([]any)
	if len(details) == 0 {
		t.Fatalf("冻结节应按记录 id 路由出明细; freeze=%v", freeze)
	}
	// 注意：列表 3 条中个别记录明细库可能暂无（110，上游同步滞后），
	// 记录概要仍在列表节，明细数允许少于列表数，故只断言非空。
	d0, _ := details[0].(map[string]any)
	if _, ok := d0["implementationMatters"]; !ok {
		t.Logf("冻结节明细字段: %v", d0)
	}
	if bank := sectionByKey(t, parsed2, "bankruptcy_detail"); bank["status"] != "no_data" {
		t.Fatalf("恒大地产破产节应 no_data; bank=%v", bank)
	}
	t.Logf("案例2（有记录）: %s", summarizeJudicial(parsed2))
}

func summarizeJudicial(parsed map[string]any) string {
	var b strings.Builder
	b.WriteString("risk=" + parsed["overall_risk"].(string))
	b.WriteString(" total=" + jsonInt(parsed["total_judicial_records"]))
	b.WriteString(" errors=" + jsonInt(parsed["error_count"]))
	sections, _ := parsed["sections"].([]any)
	for _, s := range sections {
		m := s.(map[string]any)
		b.WriteString(" " + m["key"].(string) + ":" + m["status"].(string))
	}
	return b.String()
}

func jsonInt(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.Itoa(int(f))
	}
	return "?"
}
