package keeprisk

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 2026-09-16 盲测：无指引的错误文案（"未知请求错误"/"服务不存在"）让模型只能盲目重试。
// 文案要求：给出下一步动作；107 明确"未开通 + 未计费"。
func TestStatusHintsAreActionable(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub []string
	}{
		{
			name:    "107 未开通且未计费",
			body:    `{"code":107,"message":"服务不存在"}`,
			wantSub: []string{"未开通", "未计费"},
		},
		{
			name:    "101 指向本地校验",
			body:    `{"code":101,"message":"参数错误"}`,
			wantSub: []string{"validate_keeprisk_params"},
		},
		{
			name:    "500 给出核对与反馈路径",
			body:    `{"code":500,"message":"未知请求错误"}`,
			wantSub: []string{"核对参数", "反馈接口名"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			c := newTestClient(srv.URL)
			_, err := c.CallDataAPI(testCtx("keeprisk_query"), "E_007_0501", map[string]any{"entName": "测试"})
			if err == nil {
				t.Fatal("应返回错误")
			}
			msg := err.Error()
			for _, sub := range tc.wantSub {
				if !strings.Contains(msg, sub) {
					t.Fatalf("错误文案缺少 %q: %s", sub, msg)
				}
			}
		})
	}
}
