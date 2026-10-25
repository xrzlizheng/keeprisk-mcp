package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ─────────────────── selectSections 表驱动测试 ───────────────────

func TestSelectSections(t *testing.T) {
	tests := []struct {
		name          string
		sections      string
		includeJud    bool
		wantSelected  int
		wantSkipped   int
		wantErr       bool
		errContains   string
	}{
		{
			name:         "默认全开 include_judicial=true",
			sections:     "",
			includeJud:   true,
			wantSelected: len(dueDiligenceSections),
			wantSkipped:  0,
			wantErr:      false,
		},
		{
			name:        "默认 include_judicial=false 跳过司法节",
			sections:    "",
			includeJud:  false,
			wantSkipped: 0, // will be computed
			wantErr:     false,
		},
		{
			name:         "指定 basic,shareholders 两个节",
			sections:     "basic,shareholders",
			includeJud:   true,
			wantSelected: 2,
			wantSkipped:  0,
			wantErr:      false,
		},
		{
			name:         "指定单个节 basic",
			sections:     "basic",
			includeJud:   true,
			wantSelected: 1,
			wantErr:      false,
		},
		{
			name:        "指定未知节名报错",
			sections:    "basic,nonexistent_section",
			includeJud:  true,
			wantErr:     true,
			errContains: "未知 sections",
		},
		{
			name:         "空格和多余逗号容错",
			sections:     " basic , shareholders , ",
			includeJud:   true,
			wantSelected: 2,
			wantErr:      false,
		},
		{
			name:         "显式指定司法节不受 includeJudicial=false 影响",
			sections:     "court_filing,zhongben",
			includeJud:   false,
			wantSelected: 2,
			wantSkipped:  0,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, skipped, err := selectSections(tt.sections, tt.includeJud)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" {
					msg, _ := err["message"].(string)
					if !strings.Contains(msg, tt.errContains) {
						t.Errorf("error message %q does not contain %q", msg, tt.errContains)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantSelected > 0 && len(selected) != tt.wantSelected {
				t.Errorf("selected = %d, want %d", len(selected), tt.wantSelected)
			}
			if tt.sections == "" && !tt.includeJud {
				// 验证 include_judicial=false 确实跳过了 Judicial 节
				judCount := 0
				for _, s := range dueDiligenceSections {
					if s.Judicial {
						judCount++
					}
				}
				if skipped != judCount {
					t.Errorf("skipped = %d, want %d (judicial count)", skipped, judCount)
				}
				// Risk 类不应被跳过
				for _, s := range selected {
					if s.Risk {
						found := false
						for _, sel := range selected {
							if sel.Key == s.Key {
								found = true
								break
							}
						}
						if !found {
							t.Errorf("Risk section %q should not be skipped by include_judicial=false", s.Key)
						}
					}
				}
			}
			if tt.wantSkipped > 0 && skipped != tt.wantSkipped {
				t.Errorf("skipped = %d, want %d", skipped, tt.wantSkipped)
			}
		})
	}
}

// ─────────────────── generateDigest 表驱动测试 ───────────────────

func TestGenerateDigest(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		parsed       map[string]any
		wantSeverity string
		wantLevel    string
		wantCount    int
		wantRecords  bool
	}{
		{
			name:         "basic 工商信息",
			key:          "basic",
			parsed:       map[string]any{"entName": "测试公司", "legalName": "张三", "entStatus": "在业"},
			wantSeverity: "none",
			wantLevel:    "green",
			wantRecords:  true,
		},
		{
			name:         "失信人命中 = critical/red",
			key:          "dishonest",
			parsed:       map[string]any{"total": float64(3), "data": []any{map[string]any{}, map[string]any{}, map[string]any{}}},
			wantSeverity: "critical",
			wantLevel:    "red",
			wantCount:    3,
			wantRecords:  true,
		},
		{
			name:         "终本案件命中 = critical/red",
			key:          "zhongben",
			parsed:       map[string]any{"total": float64(1)},
			wantSeverity: "critical",
			wantLevel:    "red",
			wantCount:    1,
			wantRecords:  true,
		},
		{
			name:         "裁判文书命中 = medium/yellow",
			key:          "judgment",
			parsed:       map[string]any{"total": float64(5)},
			wantSeverity: "medium",
			wantLevel:    "yellow",
			wantCount:    5,
			wantRecords:  true,
		},
		{
			name:         "行政处罚无记录 = none/green",
			key:          "admin_penalty",
			parsed:       map[string]any{"total": float64(0)},
			wantSeverity: "none",
			wantLevel:    "green",
			wantCount:    0,
			wantRecords:  false,
		},
		{
			name:         "税务评级 D = high/red",
			key:          "tax_rating",
			parsed:       map[string]any{"data": map[string]any{"taxRating": "D"}},
			wantSeverity: "high",
			wantLevel:    "red",
			wantRecords:  true,
		},
		{
			name:         "税务评级 A = low/green",
			key:          "tax_rating",
			parsed:       map[string]any{"data": map[string]any{"taxRating": "A"}},
			wantSeverity: "low",
			wantLevel:    "green",
			wantRecords:  true,
		},
		{
			name:         "舆情 >5 条 = medium/yellow",
			key:          "public_sentiment",
			parsed:       map[string]any{"total": float64(8)},
			wantSeverity: "medium",
			wantLevel:    "yellow",
			wantCount:    8,
			wantRecords:  true,
		},
		{
			name:         "股权出质命中 = medium/yellow",
			key:          "equity_pledge",
			parsed:       map[string]any{"total": float64(2)},
			wantSeverity: "medium",
			wantLevel:    "yellow",
			wantCount:    2,
			wantRecords:  true,
		},
		{
			name:         "破产重整命中 = critical/red",
			key:          "bankruptcy",
			parsed:       map[string]any{"total": float64(1)},
			wantSeverity: "critical",
			wantLevel:    "red",
			wantCount:    1,
			wantRecords:  true,
		},
		{
			name:         "开庭公告命中 = medium/yellow",
			key:          "court_hearing",
			parsed:       map[string]any{"total": float64(3)},
			wantSeverity: "medium",
			wantLevel:    "yellow",
			wantCount:    3,
			wantRecords:  true,
		},
		{
			name:         "司法协助命中 = high/red",
			key:          "judicial_assist",
			parsed:       map[string]any{"total": float64(1)},
			wantSeverity: "high",
			wantLevel:    "red",
			wantCount:    1,
			wantRecords:  true,
		},
		{
			name:         "清算信息命中 = high/red",
			key:          "liquidation",
			parsed:       map[string]any{"total": float64(2)},
			wantSeverity: "high",
			wantLevel:    "red",
			wantCount:    2,
			wantRecords:  true,
		},
		{
			name:         "税务违法命中 = high/red",
			key:          "tax_violation",
			parsed:       map[string]any{"total": float64(1)},
			wantSeverity: "high",
			wantLevel:    "red",
			wantCount:    1,
			wantRecords:  true,
		},
		{
			name:         "未知 section 走 default",
			key:          "unknown_section",
			parsed:       map[string]any{"total": float64(3)},
			wantSeverity: "none",
			wantLevel:    "green",
			wantCount:    3,
			wantRecords:  true,
		},
		{
			name:         "空响应",
			key:          "dishonest",
			parsed:       map[string]any{},
			wantSeverity: "none",
			wantLevel:    "green",
			wantCount:    0,
			wantRecords:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := generateDigest(tt.key, tt.parsed)
			if d.Severity != tt.wantSeverity {
				t.Errorf("severity = %q, want %q", d.Severity, tt.wantSeverity)
			}
			if d.RiskLevel != tt.wantLevel {
				t.Errorf("risk_level = %q, want %q", d.RiskLevel, tt.wantLevel)
			}
			if d.Count != tt.wantCount {
				t.Errorf("count = %d, want %d", d.Count, tt.wantCount)
			}
			if d.HasRecords != tt.wantRecords {
				t.Errorf("has_records = %v, want %v", d.HasRecords, tt.wantRecords)
			}
			if d.Summary == "" {
				t.Error("summary should not be empty")
			}
		})
	}
}

// ─────────────────── buildRiskSummary 测试 ───────────────────

func TestBuildRiskSummary(t *testing.T) {
	tests := []struct {
		name      string
		results   []map[string]any
		wantLevel string
		wantRed   int
	}{
		{
			name: "全绿",
			results: []map[string]any{
				{"key": "basic", "digest": sectionDigest{RiskLevel: "green", Summary: "企业=测试"}},
				{"key": "dishonest", "digest": sectionDigest{RiskLevel: "green", Count: 0}},
			},
			wantLevel: "green",
			wantRed:   0,
		},
		{
			name: "一个红色",
			results: []map[string]any{
				{"key": "basic", "digest": sectionDigest{RiskLevel: "green"}},
				{"key": "dishonest", "digest": sectionDigest{RiskLevel: "red", Count: 2, Summary: "失信人 2 条"}},
			},
			wantLevel: "red",
			wantRed:   1,
		},
		{
			name: "黄色但无红色",
			results: []map[string]any{
				{"key": "judgment", "digest": sectionDigest{RiskLevel: "yellow", Count: 3}},
				{"key": "basic", "digest": sectionDigest{RiskLevel: "green"}},
			},
			wantLevel: "yellow",
			wantRed:   0,
		},
		{
			name:      "空结果",
			results:   []map[string]any{},
			wantLevel: "green",
			wantRed:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := buildRiskSummary(tt.results)
			level, _ := summary["overall_level"].(string)
			if level != tt.wantLevel {
				t.Errorf("overall_level = %q, want %q", level, tt.wantLevel)
			}
			redCount, _ := summary["red_count"].(int)
			if redCount != tt.wantRed {
				t.Errorf("red_count = %d, want %d", redCount, tt.wantRed)
			}
		})
	}
}

// ─────────────────── buildResolveCandidates 测试 ───────────────────

func TestBuildResolveCandidates(t *testing.T) {
	svc := &Services{} // 不需要真实 catalog/client

	tests := []struct {
		name       string
		parsed     map[string]any
		keyword    string
		limit      int
		wantCount  int
		wantExact  bool
	}{
		{
			name: "标准响应结构 data.list",
			parsed: map[string]any{
				"data": map[string]any{
					"list": []any{
						map[string]any{
							"entName":       "华为技术有限公司",
							"uid":           "uid001",
							"creditCode":    "91440300MA5FXXXX",
							"legalName":     "赵明路",
							"province":      "广东",
							"city":          "深圳",
							"regStatusCode": "1",
							"regStatus":     "在业",
							"entType":       "有限责任公司",
						},
						map[string]any{
							"entName":    "华为技术服务有限公司",
							"uid":        "uid002",
							"legalName":  "徐直军",
							"province":   "广东",
							"city":       "深圳",
							"regStatus":  "存续",
						},
					},
				},
			},
			keyword:   "华为技术",
			limit:     5,
			wantCount: 2,
			wantExact: false,
		},
		{
			name: "精确匹配",
			parsed: map[string]any{
				"data": map[string]any{
					"list": []any{
						map[string]any{
							"entName":   "华为技术有限公司",
							"uid":       "uid001",
							"legalName": "赵明路",
						},
					},
				},
			},
			keyword:   "华为技术有限公司",
			limit:     5,
			wantCount: 1,
			wantExact: true,
		},
		{
			name: "limit 截断",
			parsed: map[string]any{
				"data": map[string]any{
					"list": []any{
						map[string]any{"entName": "A公司"},
						map[string]any{"entName": "B公司"},
						map[string]any{"entName": "C公司"},
					},
				},
			},
			keyword:   "公司",
			limit:     2,
			wantCount: 2,
		},
		{
			name:      "空响应",
			parsed:    map[string]any{},
			keyword:   "不存在的公司",
			limit:     5,
			wantCount: 0,
		},
		{
			name: "嵌套 items 结构",
			parsed: map[string]any{
				"data": map[string]any{
					"items": []any{
						map[string]any{"entName": "测试公司", "uid": "u1"},
					},
				},
			},
			keyword:   "测试",
			limit:     5,
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := svc.buildResolveCandidates(tt.parsed, tt.keyword, tt.limit)
			if len(candidates) != tt.wantCount {
				t.Errorf("candidates count = %d, want %d", len(candidates), tt.wantCount)
			}
			if tt.wantExact && len(candidates) > 0 {
				hint, _ := candidates[0]["match_hint"].(string)
				if hint != "exact" {
					t.Errorf("match_hint = %q, want 'exact'", hint)
				}
			}
			// 验证候选结构完整
			for i, c := range candidates {
				if _, ok := c["ent_name"]; !ok && i == 0 {
					// 至少第一个候选应有 ent_name
					if entName, ok := tt.parsed["data"].(map[string]any); ok {
						if list, ok := entName["list"].([]any); ok && len(list) > 0 {
							if m, ok := list[0].(map[string]any); ok {
								if _, hasEnt := m["entName"]; hasEnt {
									t.Errorf("candidate[0] missing ent_name when source has entName")
								}
							}
						}
					}
				}
			}
		})
	}
}

// ─────────────────── Schema 结构验证 ───────────────────

func TestSchemaStructure(t *testing.T) {
	schemas := map[string]func() map[string]any{
		"keeprisk_query":                  schemaKeepriskQuery,
		"enterprise_due_diligence_snapshot": schemaDueDiligence,
		"enterprise_basic_profile":         schemaBasicProfile,
		"batch_query_companies":            schemaBatchQuery,
		"control_penetration":             schemaControlPenetration,
		"actual_controller":               schemaActualController,
		"search_chain_enterprises":        schemaSearchChainEnterprises,
		"get_chain_nodes":                 schemaGetChainNodes,
		"search_chains":                   schemaSearchChains,
		"search_enterprises":              schemaSearchEnterprises,
		"list_categories":                 schemaListCategories,
		"search_keeprisk_apis":            schemaSearchApis,
		"get_keeprisk_api":                schemaGetApi,
		"validate_keeprisk_params":        schemaValidateParams,
		"resolve_entity":                  schemaResolveEntity,
		"enterprise_credit_score":          schemaCreditScore,
		"listed_company_profile":           schemaListedCompanyProfile,
		"supply_chain_relations":           schemaSupplyChainRelations,
		"investment_readiness_score":       schemaInvestmentReadiness,
		"industry_chain_profile":          schemaIndustryChainProfile,
		"estimate_billing":                schemaEstimateBilling,
		"enterprise_extended_profile":     schemaEnterpriseExtendedProfile,
		"listed_company_financials":       schemaListedCompanyFinancials,
		"listed_company_market":           schemaListedCompanyMarket,
		"annual_report_investments":       schemaAnnualReportInvestments,
		"intellectual_property_portfolio": schemaIntellectualPropertyPortfolio,
		"enterprise_graph_relations":      schemaEnterpriseGraphRelations,
		"operational_compliance":          schemaOperationalCompliance,
		"judicial_deep_dive":              schemaJudicialDeepDive,
		"data_badge_full":                 schemaDataBadgeFull,
		"enterprise_business_detail":      schemaEnterpriseBusinessDetail,
		"enterprise_deep_profile":         schemaEnterpriseDeepProfile,
		"listed_company_extended":         schemaListedCompanyExtended,
		"investment_analysis_suite":       schemaInvestmentAnalysisSuite,
		"enterprise_development":          schemaEnterpriseDevelopment,
		"investment_event_profile":        schemaInvestmentEventProfile,
		"enterprise_evaluation_report":    schemaEnterpriseEvaluationReport,
		"annual_report_detail":           schemaAnnualReportDetail,
		"industrial_park_search":         schemaIndustrialParkSearch,
	}

	for name, fn := range schemas {
		t.Run(name, func(t *testing.T) {
			schema := fn()
			if schema == nil {
				t.Fatal("schema is nil")
			}
			// 验证 type = "object"
			if tp, ok := schema["type"].(string); !ok || tp != "object" {
				t.Errorf("type = %v, want 'object'", schema["type"])
			}
			// 验证有 properties
			if _, ok := schema["properties"].(map[string]any); !ok {
				t.Error("missing or invalid 'properties'")
			}
			// 验证可 JSON 序列化
			b, err := json.Marshal(schema)
			if err != nil {
				t.Errorf("json.Marshal error: %v", err)
			}
			if len(b) == 0 {
				t.Error("serialized schema is empty")
			}
		})
	}
}

// TestSchemaRequiredFields 验证必填参数设置。
//
// 注意：入口工具（resolve_entity / fuzzy_search / person_risk_profile）刻意不在
// JSON Schema 声明 required——SDK 会在协议层硬拒为 -32602（无业务语义、无示例），
// 二轮盲测确认这让模型首调即失败。改为 handler 内校验并返回富错误（见 arghelp.go），
// 对应用例见 TestEntryArgAliasAndRichError。
func TestSchemaRequiredFields(t *testing.T) {
	tests := []struct {
		name     string
		schema   map[string]any
		required []string
	}{
		{"keeprisk_query", schemaKeepriskQuery(), []string{"pcode"}},
		{"control_penetration", schemaControlPenetration(), []string{"ent_name"}},
		{"actual_controller", schemaActualController(), []string{"ent_name"}},
		{"get_keeprisk_api", schemaGetApi(), []string{"pcode"}},
		{"validate_keeprisk_params", schemaValidateParams(), []string{"pcode"}},
		{"search_chain_enterprises", schemaSearchChainEnterprises(), []string{"chain"}},
		{"get_chain_nodes", schemaGetChainNodes(), []string{"chain"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqRaw, ok := tt.schema["required"]
			if !ok {
				t.Fatal("schema missing 'required' field")
			}
			reqSlice, ok := reqRaw.([]string)
			if !ok {
				t.Fatalf("'required' is not []string: %T", reqRaw)
			}
			if len(reqSlice) != len(tt.required) {
				t.Errorf("required count = %d, want %d", len(reqSlice), len(tt.required))
			}
			for _, r := range tt.required {
				found := false
				for _, s := range reqSlice {
					if s == r {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("required missing %q", r)
				}
			}
		})
	}
}

func TestSchemaAdvancedConstraints(t *testing.T) {
	tests := []struct {
		name                    string
		schema                  map[string]any
		wantAdditionalProps     bool
		wantHasAdditionalProps  bool
		wantAnyOfMinLen         int
	}{
		{
			name:                   "search_enterprises supports pass-through params",
			schema:                 schemaSearchEnterprises(),
			wantAdditionalProps:    true,
			wantHasAdditionalProps: true,
		},
		{
			name:                   "search_chain_enterprises supports pass-through params",
			schema:                 schemaSearchChainEnterprises(),
			wantAdditionalProps:    true,
			wantHasAdditionalProps: true,
		},
		{
			name:            "enterprise_basic_profile requires one entity selector",
			schema:          schemaBasicProfile(),
			wantAnyOfMinLen: 2,
		},
		{
			name:            "enterprise_credit_score requires one entity selector",
			schema:          schemaCreditScore(),
			wantAnyOfMinLen: 2,
		},
		{
			name:            "company_quick_scan accepts entity or keyword",
			schema:          schemaCompanyQuickScan(),
			wantAnyOfMinLen: 3,
		},
		{
			name:            "industrial_park_search requires park or region selector",
			schema:          schemaIndustrialParkSearch(),
			wantAnyOfMinLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantHasAdditionalProps {
				v, ok := tt.schema["additionalProperties"].(bool)
				if !ok {
					t.Fatalf("schema missing boolean additionalProperties")
				}
				if v != tt.wantAdditionalProps {
					t.Fatalf("additionalProperties = %v, want %v", v, tt.wantAdditionalProps)
				}
			}

			if tt.wantAnyOfMinLen > 0 {
				raw, ok := tt.schema["anyOf"].([]any)
				if !ok {
					t.Fatalf("schema missing anyOf")
				}
				if len(raw) < tt.wantAnyOfMinLen {
					t.Fatalf("anyOf length = %d, want >= %d", len(raw), tt.wantAnyOfMinLen)
				}
			}
		})
	}
}

func TestSchemaCriticalParameterExposure(t *testing.T) {
	tests := []struct {
		name        string
		schema      map[string]any
		properties  []string
	}{
		{
			name:   "search_enterprises exposes advanced filters",
			schema: schemaSearchEnterprises(),
			properties: []string{
				"industry_levels",
				"point",
			},
		},
		{
			name:   "search_chain_enterprises exposes advanced filters",
			schema: schemaSearchChainEnterprises(),
			properties: []string{
				"exclude_region_list",
				"score_region_map",
				"industry_levels",
				"point",
			},
		},
		{
			name:   "industrial_park_search exposes park_id",
			schema: schemaIndustrialParkSearch(),
			properties: []string{
				"park_id",
			},
		},
		{
			name:   "enterprise_evaluation_report exposes region filters",
			schema: schemaEnterpriseEvaluationReport(),
			properties: []string{
				"province",
				"city",
				"county",
				"economic_region",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props, ok := tt.schema["properties"].(map[string]any)
			if !ok {
				t.Fatal("missing properties")
			}
			for _, key := range tt.properties {
				if _, ok := props[key]; !ok {
					t.Fatalf("missing property %q", key)
				}
			}
		})
	}
}

func TestSchemaDueDiligenceDescriptionMatches26Sections(t *testing.T) {
	schema := schemaDueDiligence()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	sectionsProp, ok := props["sections"].(map[string]any)
	if !ok {
		t.Fatal("missing sections property")
	}
	desc, _ := sectionsProp["description"].(string)
	for _, want := range []string{
		"liquidation",
		"tax_violation",
		"court_hearing",
		"judicial_assist",
		"26",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("sections description missing %q: %s", want, desc)
		}
	}
}

// ─────────────────── extractCount 边界测试 ───────────────────

func TestExtractCount(t *testing.T) {
	tests := []struct {
		name   string
		parsed map[string]any
		want   int
	}{
		{"total float64", map[string]any{"total": float64(42)}, 42},
		{"totalNum", map[string]any{"totalNum": float64(10)}, 10},
		{"data array", map[string]any{"data": []any{1, 2, 3}}, 3},
		{"data.list", map[string]any{"data": map[string]any{"list": []any{1, 2}}}, 2},
		{"items", map[string]any{"items": []any{1}}, 1},
		{"empty", map[string]any{}, 0},
		{"no records", map[string]any{"total": float64(0)}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCount(tt.parsed)
			if got != tt.want {
				t.Errorf("extractCount = %d, want %d", got, tt.want)
			}
		})
	}
}

// ─────────────────── 信用评分辅助函数测试 ───────────────────

func TestScoreToLevel(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{95, "A"},
		{85, "B+"},
		{75, "B"},
		{65, "C"},
		{55, "D"},
		{40, "E"},
		{0, ""},
	}
	for _, tt := range tests {
		got := scoreToLevel(tt.score)
		if got != tt.want {
			t.Errorf("scoreToLevel(%v) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

func TestParseScoreDimension(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		title   string
		parsed  map[string]any
		wantHas bool
		wantScr float64
	}{
		{
			name:    "标准评分响应",
			key:     "financial_score",
			title:   "企业财务状况评分",
			parsed:  map[string]any{"data": map[string]any{"score": float64(78), "level": "B+", "year": "2024"}},
			wantHas: true,
			wantScr: 78,
		},
		{
			name:    "数组响应取首条",
			key:     "operational_score",
			title:   "企业经营评分",
			parsed:  map[string]any{"data": []any{map[string]any{"score": float64(82), "level": "A"}}},
			wantHas: true,
			wantScr: 82,
		},
		{
			name:    "空响应",
			key:     "innovation_score",
			title:   "企业科创评分",
			parsed:  map[string]any{"data": map[string]any{}},
			wantHas: false,
			wantScr: 0,
		},
		{
			name:    "totalScore 字段",
			key:     "comprehensive_score",
			title:   "企业综合评分",
			parsed:  map[string]any{"data": map[string]any{"totalScore": float64(75)}},
			wantHas: true,
			wantScr: 75,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dim := parseScoreDimension(tt.key, tt.title, tt.parsed)
			if dim.HasData != tt.wantHas {
				t.Errorf("has_data = %v, want %v", dim.HasData, tt.wantHas)
			}
			if dim.Score != tt.wantScr {
				t.Errorf("score = %v, want %v", dim.Score, tt.wantScr)
			}
			if dim.Title != tt.title {
				t.Errorf("title = %q, want %q", dim.Title, tt.title)
			}
		})
	}
}

func TestParseBadges(t *testing.T) {
	tests := []struct {
		name   string
		parsed map[string]any
		want   map[string]int
	}{
		{
			name:   "标准角标响应",
			parsed: map[string]any{"data": map[string]any{"judicialCount": float64(3), "businessCount": float64(12), "operationCount": float64(8), "ipCount": float64(5)}},
			want:   map[string]int{"judicial": 3, "business": 12, "operation": 8, "ip": 5},
		},
		{
			name:   "空数据",
			parsed: map[string]any{"data": map[string]any{}},
			want:   map[string]int{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			badges := parseBadges(tt.parsed)
			for k, v := range tt.want {
				if badges[k] != v {
					t.Errorf("badges[%q] = %d, want %d", k, badges[k], v)
				}
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		name   string
		parsed map[string]any
		want   int
	}{
		{
			name:   "数组内 tagName",
			parsed: map[string]any{"data": map[string]any{"list": []any{map[string]any{"tagName": "高新技术企业"}, map[string]any{"tagName": "规上企业"}}}},
			want:   2,
		},
		{
			name:   "字符串数组",
			parsed: map[string]any{"data": []any{"专精特新", "独角兽"}},
			want:   2,
		},
		{
			name:   "空数据",
			parsed: map[string]any{"data": map[string]any{}},
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := parseTags(tt.parsed)
			if len(tags) != tt.want {
				t.Errorf("tags count = %d, want %d", len(tags), tt.want)
			}
		})
	}
}

func TestBuildCreditRecommendation(t *testing.T) {
	// 2026-09-18 契约变更：绿卡需要「角标取到」+「≥2 个评分维度存活」两个前提
	// （原实现只看 composite 分值，实测飞友科技在角标解析失败、维度只剩 1 个时
	//  仍输出 green + "可快速通过"，属假绿灯）。下列 scores 刻意带两个真实维度，
	// 因为 composite 是派生项，不算存活维度。
	withDims := func(compositeScore float64) map[string]creditScoreDim {
		return map[string]creditScoreDim{
			"composite":         {Score: compositeScore, HasData: true},
			"operational_score": {Score: compositeScore + 1, HasData: true},
			"innovation_score":  {Score: compositeScore - 1, HasData: true},
		}
	}
	tests := []struct {
		name    string
		scores  map[string]creditScoreDim
		badges  map[string]int
		redFlag bool
		wantLev string
	}{
		{
			name:    "高分+角标完整+维度充足 = green",
			scores:  withDims(85),
			badges:  map[string]int{"judicial": 0, "courtTotal": 0},
			wantLev: "green",
		},
		{
			name:    "低分 = red",
			scores:  withDims(40),
			badges:  map[string]int{"judicial": 0},
			redFlag: true,
			wantLev: "red",
		},
		{
			name:    "中等分 = yellow",
			scores:  withDims(65),
			badges:  map[string]int{"judicial": 0},
			wantLev: "yellow",
		},
		{
			name:    "司法角标过高 = red",
			scores:  withDims(85),
			badges:  map[string]int{"judicial": 10},
			wantLev: "red",
		},
		{
			name:    "角标取不到 = unknown（不得给通过建议）",
			scores:  withDims(95),
			badges:  map[string]int{},
			wantLev: "unknown",
		},
		{
			name: "仅 1 个评分维度存活 = unknown（综合分不具代表性）",
			scores: map[string]creditScoreDim{
				"composite":         {Score: 95, HasData: true},
				"operational_score": {Score: 95, HasData: true},
				"innovation_score":  {Score: 0, HasData: false},
			},
			badges:  map[string]int{"judicial": 0},
			wantLev: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, rec := buildCreditRecommendation(tt.scores, tt.badges, tt.redFlag)
			if level != tt.wantLev {
				t.Errorf("level = %q, want %q", level, tt.wantLev)
			}
			if rec == "" {
				t.Error("recommendation should not be empty")
			}
		})
	}
}

// 角标解析必须支持 E_007_1391 的**嵌套**结构。
//
// 实测结构（飞友科技）：{"bussiness":{...},"intellectual":{...},
// "judicature":{"courtTotal":2,"announcementTotal":8,...},"operationalRisk":{...}}
// 旧实现只认扁平的 judicialCount/businessCount 等十个键，对嵌套结构一个都不命中
// → badges 恒空 → 司法红旗检测恒不触发（假绿灯的成因之一）。
func TestParseBadgesNested(t *testing.T) {
	parsed := map[string]any{
		"code": "100",
		"data": map[string]any{
			"judicature": map[string]any{
				"courtTotal":        float64(2),
				"announcementTotal": float64(8),
				"enforceTotal":      float64(0),
			},
			"intellectual": map[string]any{
				"entPatentInfoCount": float64(22),
				"brandCount":         float64(37),
			},
			"operationalRisk": map[string]any{
				"penaltyCount": float64(0),
				"abnormalCount": float64(0),
			},
			"bussiness": map[string]any{
				"companyChangeInfoTotal": float64(75),
			},
		},
	}
	b := parseBadges(parsed)
	if b["judicial"] != 10 {
		t.Errorf("judicial 域合计 = %d, want 10（2+8）", b["judicial"])
	}
	if b["ip"] != 59 {
		t.Errorf("ip 域合计 = %d, want 59（22+37）", b["ip"])
	}
	if b["business"] != 75 {
		t.Errorf("business 域合计 = %d, want 75", b["business"])
	}
	// 叶子计数保留原名，便于定位子项
	if b["courtTotal"] != 2 || b["announcementTotal"] != 8 {
		t.Errorf("叶子计数未保留: courtTotal=%d announcementTotal=%d", b["courtTotal"], b["announcementTotal"])
	}
	// 关键回归：司法信号必须能触发（旧实现下 judicial 恒为 0）
	if b["judicial"]+b["risk"] == 0 {
		t.Error("司法/风险信号为 0，红旗检测将失效（假绿灯回归）")
	}
}

// 嵌套角标经 parseBadges 后必须能让「有法院公告的公司」判红，而不是绿。
func TestCreditRecommendationNotGreenOnJudicialSignals(t *testing.T) {
	parsed := map[string]any{
		"code": "100",
		"data": map[string]any{
			"judicature": map[string]any{"courtTotal": float64(2), "announcementTotal": float64(8)},
		},
	}
	badges := parseBadges(parsed)
	scores := map[string]creditScoreDim{
		"composite":         {Score: 95, HasData: true},
		"operational_score": {Score: 95, HasData: true},
		"innovation_score":  {Score: 95, HasData: true},
	}
	level, _ := buildCreditRecommendation(scores, badges, false)
	if level == "green" {
		t.Fatalf("存在法院公告 2 条 + 开庭公告 8 条时不得给出 green（level=%q）", level)
	}
}

// ─────────────────── 产业链画像 digest 测试 ───────────────────

func TestBuildChainProfileDigest(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		parsed     map[string]any
		wantCount  int
		wantHasData bool
	}{
		{
			name:        "chain_analysis 有数据",
			key:         "chain_analysis",
			parsed:      map[string]any{"total": float64(5), "data": map[string]any{"chainName": "新能源", "nodeCount": float64(12), "enterpriseCount": float64(100)}},
			wantCount:   5,
			wantHasData: true,
		},
		{
			name:        "region_stats 有地区",
			key:         "region_stats",
			parsed:      map[string]any{"total": float64(3), "data": map[string]any{"list": []any{map[string]any{}, map[string]any{}}}},
			wantCount:   3,
			wantHasData: true,
		},
		{
			name:        "node_stats 空",
			key:         "node_stats",
			parsed:      map[string]any{"total": float64(0)},
			wantCount:   0,
			wantHasData: false,
		},
		{
			name:        "chain_finance 有财务数据",
			key:         "chain_finance",
			parsed:      map[string]any{"total": float64(10), "data": map[string]any{"totalRevenue": float64(5000), "totalProfit": float64(800)}},
			wantCount:   10,
			wantHasData: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := buildChainProfileDigest(tt.key, tt.parsed)
			if rc, ok := d["record_count"].(int); ok && rc != tt.wantCount {
				t.Errorf("record_count = %d, want %d", rc, tt.wantCount)
			}
			if hd, ok := d["has_data"].(bool); ok && hd != tt.wantHasData {
				t.Errorf("has_data = %v, want %v", hd, tt.wantHasData)
			}
			if d["summary"] == nil || d["summary"] == "" {
				t.Error("summary should not be empty")
			}
		})
	}
}

// ─────────────────── 企业全景画像 digest 测试 ───────────────────

func TestBuildExtendedProfileDigest(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		parsed      map[string]any
		wantHasData bool
		checkField  string
	}{
		{
			name:        "basic_info 有工商数据",
			key:         "basic_info",
			parsed:      map[string]any{"entName": "测试公司", "legalName": "张三", "entStatus": "在业"},
			wantHasData: true,
			checkField:  "ent_name",
		},
		{
			name:        "shareholders 有股东",
			key:         "shareholders",
			parsed:      map[string]any{"total": float64(3), "data": []any{map[string]any{}, map[string]any{}, map[string]any{}}},
			wantHasData: true,
			checkField:  "count",
		},
		{
			name:        "equity_pledge 无出质",
			key:         "equity_pledge",
			parsed:      map[string]any{"total": float64(0)},
			wantHasData: false,
			checkField:  "count",
		},
		{
			name:        "data_badges 有角标",
			key:         "data_badges",
			parsed:      map[string]any{"data": map[string]any{"judicialCount": float64(2)}},
			wantHasData: true,
			checkField:  "badges",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := buildExtendedProfileDigest(tt.key, tt.parsed)
			if hd, ok := d["has_data"].(bool); ok && hd != tt.wantHasData {
				t.Errorf("has_data = %v, want %v", hd, tt.wantHasData)
			}
			if tt.checkField != "" {
				if _, ok := d[tt.checkField]; !ok {
					t.Errorf("missing expected field %q in digest", tt.checkField)
				}
			}
		})
	}
}

// ─────────────────── 计费估算测试 ───────────────────

func TestEstimateBillingLogic(t *testing.T) {
	svc := &Services{}

	tests := []struct {
		name      string
		tool      string
		args      map[string]any
		wantCalls int
		wantRO    bool
	}{
		{
			name:      "只读工具 list_categories",
			tool:      "list_categories",
			args:      map[string]any{},
			wantCalls: 0,
			wantRO:    true,
		},
		{
			name:      "信用评分 = 6 次调用",
			tool:      "enterprise_credit_score",
			args:      map[string]any{},
			wantCalls: 6,
			wantRO:    false,
		},
		{
			name:      "上市公司画像 = 8 次调用",
			tool:      "listed_company_profile",
			args:      map[string]any{},
			wantCalls: 8,
			wantRO:    false,
		},
		{
			name:      "尽调默认 26 节",
			tool:      "enterprise_due_diligence_snapshot",
			args:      map[string]any{},
			wantCalls: len(dueDiligenceSections),
			wantRO:    false,
		},
		{
			name:      "尽调 include_judicial=false 跳过司法节",
			tool:      "enterprise_due_diligence_snapshot",
			args:      map[string]any{"include_judicial": false},
			wantCalls: 0, // will be computed
			wantRO:    false,
		},
		{
			name:      "批量查询 5 家企业（单次 E_007_1111）",
			tool:      "batch_query_companies",
			args:      map[string]any{"ent_names": []any{"A", "B", "C", "D", "E"}},
			wantCalls: 1,
			wantRO:    false,
		},
		{
			name:      "未知工具按 1 次",
			tool:      "unknown_tool",
			args:      map[string]any{},
			wantCalls: 1,
			wantRO:    false,
		},
		// Phase 5 聚合工具计费
		{
			name:      "上市公司财务 = 13 次调用",
			tool:      "listed_company_financials",
			args:      map[string]any{},
			wantCalls: 13,
			wantRO:    false,
		},
		{
			name:      "上市公司市场 = 15 次调用（全市场节已移除）",
			tool:      "listed_company_market",
			args:      map[string]any{},
			wantCalls: 15,
			wantRO:    false,
		},
		{
			name:      "企业年报与投资 = 4 次调用",
			tool:      "annual_report_investments",
			args:      map[string]any{},
			wantCalls: 4,
			wantRO:    false,
		},
		{
			name:      "知识产权全景 = 5 次调用",
			tool:      "intellectual_property_portfolio",
			args:      map[string]any{},
			wantCalls: 5,
			wantRO:    false,
		},
		{
			name:      "企业关联图谱 = 4 次调用",
			tool:      "enterprise_graph_relations",
			args:      map[string]any{},
			wantCalls: 4,
			wantRO:    false,
		},
		{
			name:      "经营合规 = 4 次调用",
			tool:      "operational_compliance",
			args:      map[string]any{},
			wantCalls: 4,
			wantRO:    false,
		},
		{
			name:      "司法深度 = 4 次调用",
			tool:      "judicial_deep_dive",
			args:      map[string]any{},
			wantCalls: 4,
			wantRO:    false,
		},
		{
			name:      "企业全景画像 = 9 次调用",
			tool:      "enterprise_extended_profile",
			args:      map[string]any{},
			wantCalls: 9,
			wantRO:    false,
		},
		// Phase 6 聚合工具计费
		{
			name:      "全量角标 = 4 次调用",
			tool:      "data_badge_full",
			args:      map[string]any{},
			wantCalls: 4,
			wantRO:    false,
		},
		{
			name:      "经营详情 = 8 次调用",
			tool:      "enterprise_business_detail",
			args:      map[string]any{},
			wantCalls: 8,
			wantRO:    false,
		},
		{
			name:      "深度画像补充 = 6 次调用（董监高节已移除）",
			tool:      "enterprise_deep_profile",
			args:      map[string]any{},
			wantCalls: 6,
			wantRO:    false,
		},
		{
			name:      "上市扩展 = 10 次调用",
			tool:      "listed_company_extended",
			args:      map[string]any{},
			wantCalls: 10,
			wantRO:    false,
		},
		{
			name:      "招商宝套件 = 7 次调用",
			tool:      "investment_analysis_suite",
			args:      map[string]any{},
			wantCalls: 7,
			wantRO:    false,
		},
		{
			name:      "企业发展 = 5 次调用",
			tool:      "enterprise_development",
			args:      map[string]any{},
			wantCalls: 5,
			wantRO:    false,
		},
		{
			name:      "投资事件画像 = 5 次调用",
			tool:      "investment_event_profile",
			args:      map[string]any{},
			wantCalls: 5,
			wantRO:    false,
		},
		{
			name:      "评价报告未提供地区时仅 2 次调用",
			tool:      "enterprise_evaluation_report",
			args:      map[string]any{},
			wantCalls: 2,
			wantRO:    false,
		},
		{
			name:      "年报详情 = 8 次调用",
			tool:      "annual_report_detail",
			args:      map[string]any{},
			wantCalls: 8,
			wantRO:    false,
		},
		{
			name:      "产业园区搜索按 park_name + province = 4 次调用",
			tool:      "industrial_park_search",
			args:      map[string]any{"park_name": "测试园区", "province": "安徽"},
			wantCalls: 4,
			wantRO:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := svc.EstimateBilling(tt.tool, tt.args)
			var parsed map[string]any
			if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
				t.Fatalf("json.Unmarshal error: %v", err)
			}
			isRO, _ := parsed["is_read_only"].(bool)
			if isRO != tt.wantRO {
				t.Errorf("is_read_only = %v, want %v", isRO, tt.wantRO)
			}
			calls, _ := parsed["estimated_calls"].(float64)
			if tt.tool == "enterprise_due_diligence_snapshot" && tt.args["include_judicial"] == false {
				// 验证跳过数 > 0
				skipped, _ := parsed["sections_skipped"].(float64)
				if skipped <= 0 {
					t.Error("include_judicial=false should skip judicial sections")
				}
			} else if int(calls) != tt.wantCalls {
				t.Errorf("estimated_calls = %v, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// E_007_0931 企业财务年报的取值语义与字段中文名（2026-09-18）。
//
// 依据接口文档 data 节点说明：8 个字段均为**区间范围**，取值形如 "4000,5000"
// 表示 [下界, 上界]，不是"两个含义不明的数值"。
//
// 同时该接口英文键名有明确误导性：retained_profit 文档中文为「净利润」，
// 而英文直译为「留存收益/未分配利润」——两者是完全不同的财务概念。
// 因此 digest 必须带中文字段名，否则 Agent 会按英文字面读错。
func TestAnnualFinancialRangeAndLabels(t *testing.T) {
	parsed := map[string]any{
		"code": "100",
		"data": map[string]any{
			"total_assets":     "10000,20000",
			"total_liability":  "8000,9000",
			"total_equity":     "3000,4000",
			"total_sales":      "6000,7000",
			"prime_bus_profit": "6000,7000",
			"total_profit":     "400,500",
			"retained_profit":  "500,600",
			"total_tax":        "100,200",
		},
	}
	d := buildAnnualDigest("financial_annual", parsed)

	if d["has_data"] != true {
		t.Fatalf("有 8 个字段应判 has_data=true，实际 %v", d["has_data"])
	}
	// 取值按"区间范围"原样呈现
	if d["total_assets"] != "10000,20000" {
		t.Errorf("total_assets 应原样保留区间值，实际 %v", d["total_assets"])
	}
	fmtNote, _ := d["value_format"].(string)
	if !strings.Contains(fmtNote, "区间") {
		t.Errorf("必须声明取值为区间范围，实际 %q", fmtNote)
	}
	labels, ok := d["field_labels"].(map[string]string)
	if !ok || len(labels) == 0 {
		t.Fatalf("field_labels 缺失：%v", d["field_labels"])
	}
	// 关键防误读：retained_profit 的中文名必须点明是净利润
	if !strings.Contains(labels["retained_profit"], "净利润") {
		t.Errorf("retained_profit 必须标注为净利润，实际 %q", labels["retained_profit"])
	}
	if labels["total_sales"] != "营业收入" {
		t.Errorf("total_sales 应标注为营业收入，实际 %q", labels["total_sales"])
	}
}

// 区间字段表必须与接口文档的 8 个字段一一对应（防止后续漏改）。
func TestFinancialAnnualFieldTableComplete(t *testing.T) {
	want := []string{"total_assets", "total_liability", "total_equity", "total_sales",
		"prime_bus_profit", "total_profit", "retained_profit", "total_tax"}
	got := map[string]bool{}
	for _, f := range financialAnnualFields {
		got[f.Name] = true
		if f.Label == "" {
			t.Errorf("%s 缺少中文名", f.Name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("字段数 = %d, want %d", len(got), len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("字段表缺少 %s", w)
		}
	}
}

// digest 不得出现 "has_data=true 却 record_count=0 + 0 条记录" 的自相矛盾。
//
// 实测（2026-09-18 annual_report_investments.financial_annual）：E_007_0931 返回 8 个
// 区间字段，既无 total 也无数组 → count=0；旧摘要写"0 条记录"而 has_data=true。
func TestDigestSummaryNoSelfContradiction(t *testing.T) {
	parsed := map[string]any{
		"code": "100",
		"data": map[string]any{"total_assets": "10000,20000", "retained_profit": "600,700"},
	}
	d := buildAnnualDigest("financial_annual", parsed)
	if d["has_data"] != true {
		t.Fatalf("应判有数据，实际 %v", d["has_data"])
	}
	sum, _ := d["summary"].(string)
	if strings.Contains(sum, "0 条记录") {
		t.Errorf("有数据却写 0 条记录（自相矛盾）: %q", sum)
	}
	if !strings.Contains(sum, "有数据") {
		t.Errorf("应说明有数据但无条数口径，实际 %q", sum)
	}
	if _, has := d["record_count"]; has {
		t.Errorf("无条数口径时不应输出 record_count，实际 %v", d["record_count"])
	}

	// 真空集仍须写"无数据"
	empty := buildAnnualDigest("financial_annual", map[string]any{"code": "110", "data": nil})
	if s, _ := empty["summary"].(string); !strings.Contains(s, "无数据") {
		t.Errorf("空集摘要应为无数据，实际 %q", s)
	}
}

// E_007_0401 企业图谱：载荷在 data.result 下（文档：`result 结果集`），
// 旧实现在 data 下直接找 nodes/relationships → node_count 恒取不到。
func TestGraphDigestResultNesting(t *testing.T) {
	parsed := map[string]any{
		"code": "100",
		"data": map[string]any{
			"result": map[string]any{
				"nodes": []any{
					map[string]any{"id": "1"}, map[string]any{"id": "2"},
				},
				"relationships": []any{
					map[string]any{"id": "r1"}, map[string]any{"id": "r2"}, map[string]any{"id": "r3"},
				},
			},
		},
	}
	d := buildGraphDigest("enterprise_graph", parsed)
	if d["node_count"] != 2 {
		t.Errorf("node_count = %v, want 2", d["node_count"])
	}
	if d["relationship_count"] != 3 {
		t.Errorf("relationship_count = %v, want 3", d["relationship_count"])
	}
	if d["record_count"] != 5 {
		t.Errorf("record_count 应为节点+边=5，实际 %v", d["record_count"])
	}
	if s, _ := d["summary"].(string); !strings.Contains(s, "节点 2 + 边 3") {
		t.Errorf("摘要应写明节点/边构成，实际 %q", s)
	}
	// enrichDigestPaging 不得给图类加 page_count 或改写摘要
	enrichDigestPaging(parsed, d)
	if _, has := d["page_count"]; has {
		t.Error("图类不应输出 page_count")
	}
	if !strings.Contains(d["summary"].(string), "节点 2 + 边 3") {
		t.Errorf("图类摘要不应被改写: %q", d["summary"])
	}
}

// E_017_0121 评分标度自适应：文档示例 99.99（0–100），实测飞友科技 852.88（0–1000）。
func TestScoreScaleAdaptive(t *testing.T) {
	// 0–1000 标度 → 归一
	d1 := parseScoreDimension("comprehensive_score", "企业综合评分",
		map[string]any{"data": map[string]any{"entEntireScore": 852.88}})
	if d1.Score < 80 || d1.Score > 90 {
		t.Errorf("852.88 应按 0–1000 归一为 ~85.3，实际 %v", d1.Score)
	}
	// 0–100 标度（文档示例量级）→ 原样
	d2 := parseScoreDimension("comprehensive_score", "企业综合评分",
		map[string]any{"data": map[string]any{"entEntireScore": 99.99}})
	if d2.Score < 99 || d2.Score > 100 {
		t.Errorf("99.99 应按 0–100 原样保留，实际 %v", d2.Score)
	}
}

// investment_readiness_score 的摘要字段名必须与各接口文档一致（2026-09-18）。
//
// 旧实现三处全错且症状相同——摘要恒缺失、工具不报错：
//   E_017_0121 综合评分文档字段是 entEntireScore（旧读 score）
//   E_017_0161 意愿评分文档字段是 finalScore   （旧读 score）
//   E_017_0231 意愿分析是异步任务，返回 task_id（旧读 rating，文档无此字段）
func TestInvestmentAssessmentFieldNames(t *testing.T) {
	results := []map[string]any{
		{"key": "willingness_score", "status": "ok", "title": "投资意愿评分", "data": map[string]any{
			"code": "100", "data": map[string]any{
				"finalScore": 53, "recommendations": []any{"深入了解企业财务状况"},
			}}},
		{"key": "willingness_analysis", "status": "ok", "title": "投资意愿分析", "data": map[string]any{
			"code": "100", "data": map[string]any{
				"task_id": "task_x", "message": "投资分析任务已创建", "status_url": "/task/task_x",
			}}},
		{"key": "comprehensive_score", "status": "ok", "title": "企业综合评分", "data": map[string]any{
			"code": "100", "data": map[string]any{"entEntireScore": 852.88},
		}},
	}
	out := buildInvestmentAssessment("飞友科技有限公司", results)
	secs := map[string]map[string]any{}
	for _, s := range out["sections"].([]map[string]any) {
		secs[s["key"].(string)] = s
	}
	ws := secs["willingness_score"]
	if toF(ws["score"]) != 53 {
		t.Errorf("willingness_score 应取 finalScore=53，实际 %v", ws["score"])
	}
	wa := secs["willingness_analysis"]
	if wa["task_id"] != "task_x" || wa["async_task"] != true {
		t.Errorf("willingness_analysis 应取 task_id 并标 async_task，实际 %v / %v", wa["task_id"], wa["async_task"])
	}
	if _, has := wa["rating"]; has {
		t.Error("willingness_analysis 不应输出 rating（文档无此字段）")
	}
	cs := secs["comprehensive_score"]
	sc := toF(cs["score"])
	if sc < 80 || sc > 90 {
		t.Errorf("comprehensive_score 852.88 应按 0–1000 归一为 ~85.3，实际 %v", cs["score"])
	}
}

func toF(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return -1
}

// TestAllToolSchemasNoNullFields 防回归：经真实 registerTools 注册 + 进程内传输
// 回读 tools/list，断言任何工具 inputSchema 树中不出现 null 值。
// 背景：objProp 曾把 nil props 序列化成 "properties": null，违反 JSON Schema 元schema
// （properties 出现时须为 object），下游 agent 平台对 function 定义做严格校验时报
// Invalid schema for function 'estimate_billing': null is not of type "object"，
// 涉及 estimate_billing / keeprisk_query / validate_keeprisk_params /
// search_enterprises / search_chain_enterprises 共 5 工具（2026-10-20 修复）。
// 用真实注册路径而非手工 schema 映射表：后者已落后实际工具数（40 vs 47），新工具会漏。
func TestAllToolSchemasNoNullFields(t *testing.T) {
	srv := NewServer(nil) // 注册期不解引用 svc，nil 安全；本测试只列工具不调 handler

	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx, st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// 数量下限：低于该值说明注册路径没走全（当前 47 个），防止测试静默空转
	if len(res.Tools) < 40 {
		t.Fatalf("tools/list 仅返回 %d 个工具（应 ≥40），注册不完整", len(res.Tools))
	}
	for _, tool := range res.Tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("%s: InputSchema 序列化失败: %v", tool.Name, err)
			continue
		}
		var root any
		if err := json.Unmarshal(b, &root); err != nil {
			t.Errorf("%s: InputSchema 反序列化失败: %v", tool.Name, err)
			continue
		}
		assertNoNullValues(t, tool.Name, "$", root)
	}
}

// assertNoNullValues 递归断言 schema 树中任何位置的取值非 null。
// 本仓库 schema 用到的所有 JSON Schema 关键字（type/properties/items/required/
// description/enum/anyOf/additionalProperties/minimum/maximum 等）合法取值均不含
// null，故全树禁 null 是不产生误报的最强不变量：任何 helper 漏传值（nil map/
// nil slice/nil 指针解引用出 nil）都会在此被拦下，而非漏到下游才暴露。
func assertNoNullValues(t *testing.T, tool, path string, node any) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if child == nil {
				t.Errorf("%s: %s.%s = null（下游严格校验将拒绝该工具）", tool, path, k)
				continue
			}
			assertNoNullValues(t, tool, path+"."+k, child)
		}
	case []any:
		for i, child := range v {
			if child == nil {
				t.Errorf("%s: %s[%d] = null（下游严格校验将拒绝该工具）", tool, path, i)
				continue
			}
			assertNoNullValues(t, tool, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}
