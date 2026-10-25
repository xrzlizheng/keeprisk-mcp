package auth

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

type ctxKey struct{}

// ErrMissingAppCode 服务端鉴权上下文缺失（内部不变量：HTTP 层 Middleware 通过后必写入 ctx）。
// 对客文案保持中性，不暴露内部鉴权机制细节。
var ErrMissingAppCode = errors.New("服务端鉴权上下文缺失，请稍后重试或联系管理员")

// WithAppCode stores the caller AppCode in context.
func WithAppCode(ctx context.Context, appCode string) context.Context {
	return context.WithValue(ctx, ctxKey{}, strings.TrimSpace(appCode))
}

// AppCodeFromContext returns the AppCode or empty string.
func AppCodeFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// RequireAppCode returns AppCode or ErrMissingAppCode.
func RequireAppCode(ctx context.Context) (string, error) {
	ac := AppCodeFromContext(ctx)
	if ac == "" {
		return "", ErrMissingAppCode
	}
	return ac, nil
}

// appCodeRe 允许的 AppCode 字符集：字母/数字/下划线/连字符，1-64 位。
// 校验格式可避免任意随机字符串进入 quota/cache 状态 map（内存 DoS 面）。
var appCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidAppCode 判断 AppCode 是否符合格式要求
func ValidAppCode(app string) bool {
	return appCodeRe.MatchString(app)
}

const (
	// HeaderAuthorization 入站鉴权头。客户端不再直传 AppCode；
	// 服务端经 AppCodeResolver 映射出 AppCode 后再调网关。
	HeaderAuthorization = "Authorization"
	// bearerScheme 期望的 auth-scheme。RFC 7235 §5.1：auth-scheme 大小写不敏感。
	bearerScheme = "bearer"
)

const (
	// 统一提示：主推裸 token 写法，Bearer 前缀为标准客户端兼容写法
	msgMissingAuth = `{"error":"unauthorized","message":"缺少 Authorization 请求头。请在 Header 设置 Authorization: <你的token>（也可写作 Bearer <token>）后重试。"}`
	// 统一无效文案：不区分 scheme 错/token 未登记，降低探测面
	msgInvalidAuth = `{"error":"unauthorized","message":"Authorization 无效或未登记。请检查 token 或联系管理员登记。"}`
)

// AuthOutcome 一次请求的鉴权结论。
// 外层审计中间件读不到内层写入 ctx 的值，经 outcomeSink 载体回传
// （401 与成功均回写），使 401 请求也能留痕。
type AuthOutcome struct {
	AppCode string // 映射后的真实 AppCode（非空=通过）
	TokenFP string // TokenFingerprint；完整 Authorization 绝不落盘
	Failure string // "" | "missing" | "scheme" | "unmatched" | "format"
}

// outcomeSink 由 HTTP 层的 statusRecorder 实现；auth 不依赖 audit 包。
type outcomeSink interface{ SetAuthOutcome(o AuthOutcome) }

// Middleware 以 Authorization: Bearer <token>（或裸 token）鉴权，
// 经 resolver 映射出 AppCode 后写入 ctx 供下游（网关调用/配额/缓存/审计）使用。
//
// 客户端 AppCode/X-AppCode 头不再作为凭证：即使携带也忽略，
// 防止绕过映射直接以网关身份调用。
func Middleware(resolver AppCodeResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var outcome AuthOutcome
		defer func() {
			if s, ok := w.(outcomeSink); ok {
				s.SetAuthOutcome(outcome)
			}
		}()

		token := strings.TrimSpace(r.Header.Get(HeaderAuthorization))
		if token == "" {
			outcome.Failure = "missing"
			unauthorized(w, msgMissingAuth)
			return
		}
		// 剥离可选 scheme（大小写不敏感的 bearer）；其余 scheme（Basic/Digest 等）拒绝
		if i := strings.IndexAny(token, " \t"); i >= 0 {
			scheme := strings.ToLower(strings.TrimSpace(token[:i]))
			rest := strings.TrimSpace(token[i+1:])
			if scheme != bearerScheme || rest == "" {
				outcome.Failure = "scheme"
				unauthorized(w, msgInvalidAuth)
				return
			}
			token = rest
		}
		appCode, ok := resolver.Resolve(r.Context(), token)
		if !ok {
			outcome.Failure = "unmatched"
			outcome.TokenFP = TokenFingerprint(token)
			unauthorized(w, msgInvalidAuth)
			return
		}
		if !ValidAppCode(appCode) {
			// 启动期已校验，此处为防御映射值脏数据
			outcome.Failure = "format"
			outcome.TokenFP = TokenFingerprint(token)
			unauthorized(w, msgInvalidAuth)
			return
		}
		outcome.AppCode = appCode
		outcome.TokenFP = TokenFingerprint(token)
		next.ServeHTTP(w, r.WithContext(WithAppCode(r.Context(), appCode)))
	})
}

func unauthorized(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(body))
}
