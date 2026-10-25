package mcp

import (
	"context"
	"sync"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildEnterpriseSearchParams(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{
			name: "basic keyword and region",
			args: map[string]any{
				"keyword":        "百度",
				"search_in":      "1",
				"province":       "北京",
				"city":           "海淀区",
				"industry":       "科技推广和应用服务业",
				"company_status": "在业",
				"page":           2,
				"page_size":      30,
			},
			want: map[string]any{
				"pageNo":   2,
				"pageSize": 30,
				"searchKey": map[string]any{
					"type": "1",
					"text": "百度",
				},
				"region": []map[string]string{
					{"pr": "北京", "ci": "海淀区"},
				},
				"industry": []map[string]string{
					{"oneLevel": "科技推广和应用服务业"},
				},
				"companyState": []int{1},
			},
		},
		{
			name: "company ids and financial ranges",
			args: map[string]any{
				"company_ids":             []any{"cid1", "cid2"},
				"assets_range":            []any{1, 2, 3},
				"sales_range":             []any{4, 5},
				"retained_profit_range":   []any{1, 2},
				"sales_rate":              []any{10, 11},
				"retained_profit_rate":    []any{20},
				"high_quality_ent":        []any{1, 3, 5},
				"capital_type":            []any{1, 2},
				"finance":                 []any{1, 7, 13},
				"listed":                  []any{3, 5, 10},
				"technology":              []any{1, 11},
				"exist_type":              []any{1, 2},
				"no_exist_type":           []any{3, 4},
				"org_type":                []any{0, 1},
				"sort_order":              "desc",
				"field_sort":              4,
				"sort_type":               false,
			},
			want: map[string]any{
				"pageNo":       1,
				"pageSize":     20,
				"companyIdList": []string{"cid1", "cid2"},
				"assetsRange":  []int{1, 2, 3},
				"salesRange":   []int{4, 5},
				"retainedProfitRange": []int{1, 2},
				"salesRate":            []int{10, 11},
				"retainedProfitRate":   []int{20},
				"highQualityEnt":       []int{1, 3, 5},
				"capitalType":          []int{1, 2},
				"finance":              []int{1, 7, 13},
				"listed":               []int{3, 5, 10},
				"technology":           []int{1, 11},
				"existType":            []int{1, 2},
				"noExistType":          []int{3, 4},
				"orgType":              []int{0, 1},
				"sortOrder":            "desc",
				"fieldSort":            4,
				"sortType":             false,
			},
		},
		{
			name: "string arrays and range objects",
			args: map[string]any{
				"company_type":                     []any{"有限责任公司", "股份有限公司"},
				"ent_org_type":                     "有限责任公司,股份有限公司",
				"pat_types":                        []string{"发明专利", "实用新型"},
				"honor_type":                       []string{"国家级", "省级"},
				"ent_scale_list":                   []string{"大型", "中型"},
				"reg_capital_garden":               []any{map[string]any{"from": "100", "to": "1000"}, map[string]any{"from": "1000", "to": "10000"}},
				"establish_times":                  []any{map[string]any{"from": "2020-01-01", "to": "2023-12-31"}},
				"contributors_ins":                 []any{map[string]any{"from": 10, "to": 100}},
				"staff_num_list_map":               []any{map[string]any{"from": 1, "to": 50}},
				"comprehensive_score_list_map":     []any{map[string]any{"from": 600, "to": 1000}},
				"innovation_score_list_map":        []any{map[string]any{"from": 60, "to": 100}},
				"real_cap_list_map":                []any{map[string]any{"from": "500", "to": "5000"}},
				"external_city_times":              []any{map[string]any{"from": "2021-01-01", "to": "2022-01-01"}},
				"honor_time_map":                   map[string]any{"from": "2023-01-01", "to": "2023-12-31"},
			},
			want: map[string]any{
				"pageNo":   1,
				"pageSize": 20,
				"companyType": []string{"有限责任公司", "股份有限公司"},
				"entOrgType":  []string{"有限责任公司", "股份有限公司"},
				"patTypes":    []string{"发明专利", "实用新型"},
				"honorType":   []string{"国家级", "省级"},
				"entScaleList": []string{"大型", "中型"},
				"regCapitalGarden": []map[string]string{
					{"from": "100", "to": "1000"},
					{"from": "1000", "to": "10000"},
				},
				"establishTimes": []map[string]string{
					{"from": "2020-01-01", "to": "2023-12-31"},
				},
				"contributorsIns": []map[string]string{
					{"from": "10", "to": "100"},
				},
				"staffNumListMap": []map[string]string{
					{"from": "1", "to": "50"},
				},
				"comprehensiveScoreListMap": []map[string]string{
					{"from": "600", "to": "1000"},
				},
				"innovationScoreListMap": []map[string]string{
					{"from": "60", "to": "100"},
				},
				"realCapListMap": []map[string]string{
					{"from": "500", "to": "5000"},
				},
				"externalCityTimes": []map[string]string{
					{"from": "2021-01-01", "to": "2022-01-01"},
				},
				"honorTimeMap": map[string]string{
					"from": "2023-01-01",
					"to":   "2023-12-31",
				},
			},
		},
		{
			name: "page size capped at 50",
			args: map[string]any{"page_size": 200},
			want: map[string]any{"pageNo": 1, "pageSize": 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildEnterpriseSearchParams(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildEnterpriseSearchParams() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildChainSearchParams(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{
			name: "basic chain and region",
			args: map[string]any{
				"chain":          "人工智能产业链",
				"node_code":      "node1",
				"province":       "江苏",
				"city":           "苏州",
				"county":         "工业园区",
				"company_status": "在业",
				"page":           1,
				"page_size":      25,
			},
			want: map[string]any{
				"chainHis": "人工智能产业链",
				"nodeCode": "node1",
				"pageNo":   1,
				"pageSize": 25,
				"regionList": []map[string]string{
					{"province": "江苏", "city": "苏州", "county": "工业园区"},
				},
				"companyState": []string{"1"},
			},
		},
		{
			name: "fuzzy fields and code tables",
			args: map[string]any{
				"chain":              "人工智能产业链",
				"keyword":            "百度",
				"business_scope":     "人工智能",
				"reg_addr":           "北京",
				"legal_person_names": "李彦宏",
				"credit_code":        "91110000802100433B",
				"economic_region":    "京津冀",
				"binding_type":       1,
				"search_logic_type":  2,
				"capital_type":       []any{1, 2},
				"listed":             []any{3, 5},
				"finance":            []any{1, 2},
				"total_sales_range":  []any{2, 3},
				"total_taxation_range": []any{4, 5},
				"technology_state":   []any{1, 2},
				"exist_type":         []any{1},
				"no_exist_type":      []any{2},
				"sort_order":         "asc",
				"field_sort":         2,
				"sort_type":          true,
			},
			want: map[string]any{
				"chainHis":   "人工智能产业链",
				"pageNo":     1,
				"pageSize":   20,
				"entName":    "百度",
				"businessScope": map[string]any{"eq": "人工智能"},
				"regAddr":    map[string]any{"eq": "北京"},
				"legalPersonNames": map[string]any{"eq": "李彦宏"},
				"creditCode": "91110000802100433B",
				"economicRegion": "京津冀",
				"bindingType": 1,
				"type": 2,
				"capitalType": []int{1, 2},
				"listed":      []int{3, 5},
				"finance":     []int{1, 2},
				"totalSalesRange": []int{2, 3},
				"totalTaxationRange": []int{4, 5},
				"technologyState": []int{1, 2},
				"existType":    []int{1},
				"noExistType":  []int{2},
				"sortOrder":    "asc",
				"sort":         map[string]any{"fieldSort": 2},
				"sortType":     true,
			},
		},
		{
			name: "range objects",
			args: map[string]any{
				"chain":           "人工智能产业链",
				"reg_capital":     []any{map[string]any{"from": 100, "to": 1000}},
				"contributors_ins": []any{map[string]any{"from": 10, "to": 100}},
				"establish_times": []any{map[string]any{"from": "2020-01-01", "to": "2021-01-01"}},
				"chain_rank_list": []any{map[string]any{"from": "0.1", "to": "0.5"}},
			},
			want: map[string]any{
				"chainHis":    "人工智能产业链",
				"pageNo":      1,
				"pageSize":    20,
				"regCapital":  []map[string]string{{"from": "100", "to": "1000"}},
				"contributorsIns": []map[string]string{{"from": "10", "to": "100"}},
				"establishTimes":  []map[string]string{{"from": "2020-01-01", "to": "2021-01-01"}},
				"chainRankList":   []map[string]string{{"from": "0.1", "to": "0.5"}},
			},
		},
		{
			name: "industry fallback",
			args: map[string]any{
				"chain":   "人工智能产业链",
				"industry": "制造业",
			},
			want: map[string]any{
				"chainHis": "人工智能产业链",
				"pageNo":   1,
				"pageSize": 20,
				"industryList": []map[string]string{{"oneLevel": "制造业"}},
			},
		},
		{
			name: "industry levels with four levels",
			args: map[string]any{
				"chain": "人工智能产业链",
				"industry_levels": []any{
					map[string]any{"oneLevel": "制造业", "twoLevel": "通用设备制造业", "treeLevel": "通用零部件制造", "fourLevel": "轴承制造"},
				},
			},
			want: map[string]any{
				"chainHis": "人工智能产业链",
				"pageNo":   1,
				"pageSize": 20,
				"industryList": []map[string]string{{"oneLevel": "制造业", "twoLevel": "通用设备制造业", "treeLevel": "通用零部件制造", "fourLevel": "轴承制造"}},
			},
		},
		{
			name: "industry levels shorthand string",
			args: map[string]any{
				"chain": "人工智能产业链",
				"industry_levels": "制造业|通用设备制造业|通用零部件制造",
			},
			want: map[string]any{
				"chainHis": "人工智能产业链",
				"pageNo":   1,
				"pageSize": 20,
				"industryList": []map[string]string{{"oneLevel": "制造业", "twoLevel": "通用设备制造业", "treeLevel": "通用零部件制造"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildChainSearchParams(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildChainSearchParams() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStringArrayArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want []string
	}{
		{"missing", map[string]any{}, "k", nil},
		{"string slice", map[string]any{"k": []string{"a", "b"}}, "k", []string{"a", "b"}},
		{"any slice", map[string]any{"k": []any{"a", "b"}}, "k", []string{"a", "b"}},
		{"comma string", map[string]any{"k": "a, b"}, "k", []string{"a", "b"}},
		{"skip empty", map[string]any{"k": []any{"a", "", "b"}}, "k", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringArrayArg(tt.args, tt.key)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stringArrayArg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRangeArrayArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want []map[string]string
	}{
		{
			name: "any slice maps",
			args: map[string]any{"k": []any{map[string]any{"from": 1, "to": 2}}},
			key:  "k",
			want: []map[string]string{{"from": "1", "to": "2"}},
		},
		{
			name: "string shorthand",
			args: map[string]any{"k": "1,2|3,4"},
			key:  "k",
			want: []map[string]string{{"from": "1", "to": "2"}, {"from": "3", "to": "4"}},
		},
		{
			name: "missing",
			args: map[string]any{},
			key:  "k",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rangeArrayArg(tt.args, tt.key)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("rangeArrayArg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRangeMapArg(t *testing.T) {
	got := rangeMapArg(map[string]any{"k": map[string]any{"from": 1, "to": 2}}, "k")
	want := map[string]string{"from": "1", "to": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rangeMapArg() = %v, want %v", got, want)
	}
	if got := rangeMapArg(map[string]any{}, "missing"); got != nil {
		t.Errorf("rangeMapArg() = %v, want nil", got)
	}
}

func TestBuildPointParamSupportsPolygonLocal(t *testing.T) {
	args := map[string]any{
		"point_type": 2,
		"point_local": []any{
			map[string]any{"lon": "116.1", "lat": "39.1"},
			map[string]any{"lon": "116.2", "lat": "39.2"},
			map[string]any{"lon": "116.3", "lat": "39.3"},
		},
		"point_sort_order": "asc",
	}

	want := map[string]any{
		"type": 2,
		"local": []map[string]string{
			{"lon": "116.1", "lat": "39.1"},
			{"lon": "116.2", "lat": "39.2"},
			{"lon": "116.3", "lat": "39.3"},
		},
		"sortOrder": "asc",
	}

	got := buildPointParam(args)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildPointParam() = %#v, want %#v", got, want)
	}
}

func TestAnnualReportDetailTreatsErrorPayloadAsError(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		if pcode == "E_007_2901" {
			return mustJSON(map[string]any{"error": "api", "message": "upstream unavailable"})
		}
		return mustJSON(map[string]any{"code": 100})
	}

	raw := svc.AnnualReportDetail(context.Background(), "测试企业", "", "")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if parsed["status"] != "error" {
		t.Fatalf("status = %v, want error; raw=%s", parsed["status"], raw)
	}
}

func TestIndustrialParkSearchUsesParkIDForEnterpriseList(t *testing.T) {
	svc := &Services{}
	var enterpriseParams map[string]any
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		switch pcode {
		case "E_007_0971":
			return mustJSON(map[string]any{
				"code": 100,
				"data": map[string]any{
					"list": []any{
						map[string]any{"id": "park-001", "parkName": "测试园区"},
					},
				},
			})
		case "E_007_0991":
			enterpriseParams = params
			return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}}})
		case "E_007_1011":
			return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}}})
		default:
			return mustJSON(map[string]any{"code": 100})
		}
	}

	_ = svc.IndustrialParkSearch(context.Background(), "测试园区", "", "")

	if enterpriseParams == nil {
		t.Fatal("expected E_007_0991 to be called")
	}
	if got := enterpriseParams["parkId"]; got != "park-001" {
		t.Fatalf("E_007_0991 parkId = %v, want park-001; params=%v", got, enterpriseParams)
	}
}

func TestIndustrialParkSearchMarksErrorPayloadAsError(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		switch pcode {
		case "E_007_0971":
			return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{map[string]any{"id": "park-001"}}}})
		case "E_007_0991":
			return mustJSON(map[string]any{"error": "api", "message": "bad park id"})
		case "E_007_1011":
			return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}}})
		default:
			return mustJSON(map[string]any{"code": 100})
		}
	}

	raw := svc.IndustrialParkSearch(context.Background(), "测试园区", "", "")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	nameResults, ok := parsed["name_results"].([]any)
	if !ok || len(nameResults) == 0 {
		t.Fatalf("missing name_results: %s", raw)
	}

	foundError := false
	for _, item := range nameResults {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if entry["pcode"] == "E_007_0991" {
			foundError = entry["status"] == "error"
		}
	}
	if !foundError {
		t.Fatalf("expected E_007_0991 status=error; raw=%s", raw)
	}
}

func TestEnterpriseEvaluationReportSkipsRegionSectionsWithoutRegion(t *testing.T) {
	svc := &Services{}
	var mu sync.Mutex
	var pcodes []string
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		// 本工具并发调用聚合节，共享 slice 必须加锁
		mu.Lock()
		pcodes = append(pcodes, pcode)
		mu.Unlock()
		return mustJSON(map[string]any{"code": 100, "data": map[string]any{"list": []any{}}})
	}

	raw := svc.EnterpriseEvaluationReport(context.Background(), "测试企业", "")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if got := int(parsed["api_calls"].(float64)); got != 2 {
		t.Fatalf("api_calls = %d, want 2; raw=%s", got, raw)
	}
	if got := int(parsed["skipped_count"].(float64)); got != 3 {
		t.Fatalf("skipped_count = %d, want 3; raw=%s", got, raw)
	}
	mu.Lock()
	called := append([]string(nil), pcodes...)
	mu.Unlock()
	if len(called) != 2 {
		t.Fatalf("called pcodes = %v, want exactly 2 enterprise-level calls", called)
	}
}

func TestGenerateDigestBasicReadsEnvelopeData(t *testing.T) {
	// 网关信封 {code, message, data:{...}}：企业字段在 data 下，顶层兜底
	env := map[string]any{
		"code": 100,
		"data": map[string]any{"entName": "测试科技有限公司", "legalName": "张三", "entStatus": "在业"},
	}
	d := generateDigest("basic", env)
	want := "企业=测试科技有限公司 法人=张三 状态=在业"
	if d.Summary != want {
		t.Fatalf("summary = %q, want %q", d.Summary, want)
	}

	// data 为单元素数组形态
	envList := map[string]any{"code": 100, "data": []any{map[string]any{"entName": "A公司", "legalName": "李四"}}}
	d2 := generateDigest("basic", envList)
	if got := d2.Summary; got != "企业=A公司 法人=李四 状态=" {
		t.Fatalf("summary(list) = %q", got)
	}

	// 顶层字段兜底（已解包结构）
	d3 := generateDigest("basic", map[string]any{"entName": "B公司"})
	if got := d3.Summary; got != "企业=B公司 法人= 状态=" {
		t.Fatalf("summary(fallback) = %q", got)
	}
}

func TestDueDiligenceBillingExcludesLocalValidation(t *testing.T) {
	svc := &Services{}
	svc.QueryFn = func(ctx context.Context, pcode string, params map[string]any) string {
		switch pcode {
		case "E_007_0501": // basic：网关成功
			return mustJSON(map[string]any{"code": 100, "data": map[string]any{"entName": "A"}})
		case "E_007_0901": // shareholders：本地校验失败，未触达网关
			return mustJSON(map[string]any{"error": "validation", "message": "缺少必填字段: entName"})
		default: // key_personnel：网关错误（触达网关，计费）
			return mustJSON(map[string]any{"error": "api", "message": "code=109 资金不足"})
		}
	}

	raw := svc.DueDiligenceSnapshot(context.Background(), "A", "", "basic,shareholders,key_personnel", true, 2, true)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if got := int(parsed["billing_units"].(float64)); got != 2 {
		t.Fatalf("billing_units = %d, want 2（basic 计费 + key_personnel 网关错误计费，shareholders 本地校验不计费）; raw=%s", got, raw)
	}
	for _, s := range parsed["sections"].([]any) {
		sec := s.(map[string]any)
		if sec["key"] == "shareholders" {
			if b, ok := sec["billable"]; !ok || b != false {
				t.Fatalf("shareholders billable = %v, want false", sec["billable"])
			}
		}
	}
}
