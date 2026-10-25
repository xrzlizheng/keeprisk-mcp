package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubResolver 固定映射表，供 Middleware 测试。
type stubResolver struct{ m map[string]string }

func (s stubResolver) Resolve(_ context.Context, token string) (string, bool) {
	app, ok := s.m[token]
	return app, ok
}

// sinkRecorder 实现 outcomeSink，捕获鉴权结论。
type sinkRecorder struct {
	*httptest.ResponseRecorder
	outcome AuthOutcome
	got     bool
}

func (s *sinkRecorder) SetAuthOutcome(o AuthOutcome) { s.outcome = o; s.got = true }

func newSink() *sinkRecorder { return &sinkRecorder{ResponseRecorder: httptest.NewRecorder()} }

func testHandler(t *testing.T, check func(r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// 有效 Bearer token：200，ctx 写入映射后的 AppCode，outcome 回传真实 AppCode。
func TestMiddlewareBearerTokenOK(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok_alice": "AppCodeAlice"}}
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer tok_alice")

	Middleware(resolver, testHandler(t, func(r *http.Request) {
		if got := AppCodeFromContext(r.Context()); got != "AppCodeAlice" {
			t.Fatalf("ctx 应写入映射后的 AppCode; got=%q", got)
		}
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("有效 Bearer token 应 200; got=%d body=%s", rec.Code, rec.Body.String())
	}
	if !rec.got || rec.outcome.AppCode != "AppCodeAlice" || rec.outcome.Failure != "" {
		t.Fatalf("outcome 应回传 AppCode; got=%+v", rec.outcome)
	}
	if rec.outcome.TokenFP == "" || rec.outcome.TokenFP == "tok_alice" {
		t.Fatalf("outcome.TokenFP 应为指纹而非原文; got=%q", rec.outcome.TokenFP)
	}
}

// 裸 token（无 Bearer 前缀）同样接受。
func TestMiddlewareBareTokenOK(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok_bob": "AppCodeBob"}}
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "tok_bob")
	Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("裸 token 应 200; got=%d", rec.Code)
	}
}

// RFC 7235：auth-scheme 大小写不敏感。
func TestMiddlewareBearerSchemeCaseInsensitive(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok_c": "AppCodeC"}}
	for _, h := range []string{"bearer tok_c", "BEARER tok_c", "BeArEr tok_c"} {
		rec := newSink()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", h)
		Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("scheme %q 大小写不敏感应 200; got=%d", h, rec.Code)
		}
	}
}

// 其他 scheme（Basic 等）拒绝。
func TestMiddlewareRejectsOtherScheme(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok": "AppCodeX"}}
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Basic scheme 应 401; got=%d", rec.Code)
	}
	if !rec.got || rec.outcome.Failure != "scheme" {
		t.Fatalf("outcome.Failure 应为 scheme; got=%+v", rec.outcome)
	}
	if strings.Contains(rec.Body.String(), "dXNlcjpwYXNz") {
		t.Fatalf("401 文案不应回显凭证; body=%s", rec.Body.String())
	}
}

// 未登记 token：401 + 指纹留痕，文案统一（不区分未登记/格式错）。
func TestMiddlewareUnmatchedToken(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok_alice": "AppCodeAlice"}}
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer intruder_token")
	Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登记 token 应 401; got=%d", rec.Code)
	}
	if !rec.got || rec.outcome.Failure != "unmatched" {
		t.Fatalf("outcome.Failure 应为 unmatched; got=%+v", rec.outcome)
	}
	if rec.outcome.TokenFP != TokenFingerprint("intruder_token") {
		t.Fatalf("outcome.TokenFP 应为指纹; got=%q", rec.outcome.TokenFP)
	}
	if strings.Contains(rec.Body.String(), "intruder_token") {
		t.Fatalf("401 文案不应回显 token; body=%s", rec.Body.String())
	}
}

// 缺失 Authorization 头：401 missing。
func TestMiddlewareMissingHeader(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok": "AppCodeX"}}
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺失 Authorization 应 401; got=%d", rec.Code)
	}
	if !rec.got || rec.outcome.Failure != "missing" {
		t.Fatalf("outcome.Failure 应为 missing; got=%+v", rec.outcome)
	}
}

// 旧凭证 AppCode/X-AppCode 不再被信任：仅携带时必须 401（防绕过映射）。
func TestMiddlewareIgnoresAppCodeHeaders(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"tok": "AppCodeX"}}
	for _, h := range [][2]string{
		{"AppCode", "AppCodeX"},
		{"X-AppCode", "AppCodeX"},
	} {
		rec := newSink()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set(h[0], h[1])
		Middleware(resolver, testHandler(t, nil)).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("仅带 %s 头应 401; got=%d", h[0], rec.Code)
		}
	}
	// 同时携带 AppCode 与有效 Authorization：AppCode 被忽略，Authorization 生效
	rec := newSink()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("AppCode", "ForgedAppCode")
	req.Header.Set("Authorization", "Bearer tok")
	Middleware(resolver, testHandler(t, func(r *http.Request) {
		if got := AppCodeFromContext(r.Context()); got != "AppCodeX" {
			t.Fatalf("应以 Authorization 映射结果为准; got=%q", got)
		}
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Authorization 有效时应 200（AppCode 头被忽略）; got=%d", rec.Code)
	}
}

// MapResolver：JSON 与简写两种格式均可解析。
func TestEnvMapResolverFormats(t *testing.T) {
	for _, spec := range []string{
		`{"tok_alice":"AppCodeAlice","tok_bob":"AppCodeBob"}`,
		"tok_alice:AppCodeAlice,tok_bob:AppCodeBob",
		" tok_alice:AppCodeAlice , tok_bob:AppCodeBob ",
	} {
		r, err := NewMapResolverFromString(spec)
		if err != nil {
			t.Fatalf("spec %q 应解析成功: %v", spec, err)
		}
		if r.Len() != 2 {
			t.Fatalf("spec %q 应有 2 组映射; got=%d", spec, r.Len())
		}
		if ac, ok := r.Resolve(context.Background(), "tok_alice"); !ok || ac != "AppCodeAlice" {
			t.Fatalf("spec %q 解析结果不符; got=%q ok=%v", spec, ac, ok)
		}
		if _, ok := r.Resolve(context.Background(), "unknown"); ok {
			t.Fatalf("未登记 token 不应命中")
		}
	}
}

// MapResolver：空值/非法格式/非法 AppCode 一律 fail-fast。
func TestEnvMapResolverFailFast(t *testing.T) {
	for _, spec := range []string{
		"",
		"   ",
		"{invalid json",
		"no-colon-entry",
		"tok_alice:",
		":AppCodeAlice",
		`{"tok_alice":"非法字符!"}`,
	} {
		if _, err := NewMapResolverFromString(spec); err == nil {
			t.Fatalf("spec %q 应报错（fail-fast）", spec)
		}
	}
}

// Reload：合法换表立即生效（新增+吊销）；非法 spec 报错且保留旧表（不产生全量 401）。
func TestMapResolverReload(t *testing.T) {
	r, err := NewMapResolverFromString(`{"tok_a":"AppCodeA"}`)
	if err != nil {
		t.Fatalf("初始加载应成功: %v", err)
	}
	n, err := r.Reload(`{"tok_a":"AppCodeA","tok_b":"AppCodeB"}`)
	if err != nil || n != 2 {
		t.Fatalf("合法重载应成功且数量为 2; got n=%d err=%v", n, err)
	}
	if ac, ok := r.Resolve(context.Background(), "tok_b"); !ok || ac != "AppCodeB" {
		t.Fatalf("新增 token 应立即生效; got=%q ok=%v", ac, ok)
	}
	if r.Len() != 2 {
		t.Fatalf("Len 应为 2; got=%d", r.Len())
	}

	if _, err := r.Reload(`{invalid`); err == nil {
		t.Fatalf("非法 spec 应报错")
	}
	if _, ok := r.Resolve(context.Background(), "tok_b"); !ok {
		t.Fatalf("失败重载不应改动既有映射")
	}
	if r.Len() != 2 {
		t.Fatalf("失败重载后 Len 应仍为 2; got=%d", r.Len())
	}
}

// 吊销语义：Reload 换表后被移除的 token 立即失效。
func TestMapResolverReloadRevokes(t *testing.T) {
	r, err := NewMapResolverFromString(`{"tok_a":"AppCodeA"}`)
	if err != nil {
		t.Fatalf("初始加载应成功: %v", err)
	}
	if _, err := r.Reload(`{"tok_b":"AppCodeB"}`); err != nil {
		t.Fatalf("重载应成功: %v", err)
	}
	if _, ok := r.Resolve(context.Background(), "tok_a"); ok {
		t.Fatalf("已移除 token 应立即失效")
	}
	if ac, ok := r.Resolve(context.Background(), "tok_b"); !ok || ac != "AppCodeB" {
		t.Fatalf("新表映射应生效; got=%q ok=%v", ac, ok)
	}
}

// 文件来源：加载、SIGHUP 重载、坏文件保留旧表。
func TestMapResolverFromFileReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth_map.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
	}
	write(`{"tok_a":"AppCodeA"}`)
	r, err := NewMapResolverFromFile(path)
	if err != nil {
		t.Fatalf("文件加载应成功: %v", err)
	}
	if r.Source() != path {
		t.Fatalf("Source 应为文件路径; got=%q", r.Source())
	}

	write(`{"tok_a":"AppCodeA","tok_b":"AppCodeB"}`)
	n, err := r.ReloadFromFile(path)
	if err != nil || n != 2 {
		t.Fatalf("文件重载应成功且数量为 2; got n=%d err=%v", n, err)
	}
	if _, ok := r.Resolve(context.Background(), "tok_b"); !ok {
		t.Fatalf("重载后新增 token 应生效")
	}

	write(`not json`)
	if _, err := r.ReloadFromFile(path); err == nil {
		t.Fatalf("坏文件重载应报错")
	}
	if _, ok := r.Resolve(context.Background(), "tok_b"); !ok {
		t.Fatalf("坏文件重载后旧表应保留")
	}
}

// 文件缺失/为空 fail-fast。
func TestMapResolverFromFileFailFast(t *testing.T) {
	if _, err := NewMapResolverFromFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatalf("缺失文件应报错")
	}
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	if _, err := NewMapResolverFromFile(path); err == nil {
		t.Fatalf("空文件应报错")
	}
}

// 来源优先级：配置文件时文件为唯一来源（env 被忽略）；文件非法 fail-fast
// 而非静默退回 env（避免已吊销 token 继续有效）；未配置文件时退回 env。
func TestMapResolverFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth_map.json")
	if err := os.WriteFile(path, []byte(`{"tok_file":"AppCodeFile"}`), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	t.Setenv(EnvAuthMapFileKey, path)
	t.Setenv(envAuthMapKey, `{"tok_env":"AppCodeEnv"}`)

	r, err := NewMapResolver()
	if err != nil {
		t.Fatalf("文件模式加载应成功: %v", err)
	}
	if ac, ok := r.Resolve(context.Background(), "tok_file"); !ok || ac != "AppCodeFile" {
		t.Fatalf("文件映射应生效; got=%q ok=%v", ac, ok)
	}
	if _, ok := r.Resolve(context.Background(), "tok_env"); ok {
		t.Fatalf("文件模式下 env 映射不应生效")
	}

	if err := os.WriteFile(path, []byte(`{bad`), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	if _, err := NewMapResolver(); err == nil {
		t.Fatalf("文件非法应 fail-fast，不应退回 env")
	}

	t.Setenv(EnvAuthMapFileKey, "")
	r2, err := NewMapResolver()
	if err != nil {
		t.Fatalf("env 兜底加载应成功: %v", err)
	}
	if ac, ok := r2.Resolve(context.Background(), "tok_env"); !ok || ac != "AppCodeEnv" {
		t.Fatalf("未配置文件时应退回 env; got=%q ok=%v", ac, ok)
	}
}

// TokenFingerprint：确定性、非原文、固定 12 位 hex。
func TestTokenFingerprint(t *testing.T) {
	a := TokenFingerprint("intruder_token")
	b := TokenFingerprint("intruder_token")
	if a != b {
		t.Fatalf("指纹应确定; %q != %q", a, b)
	}
	if a == "intruder_token" || strings.Contains(a, "_") {
		t.Fatalf("指纹不应为原文; got=%q", a)
	}
	if len(a) != 12 {
		t.Fatalf("指纹应为 12 位 hex; got=%q", a)
	}
}
