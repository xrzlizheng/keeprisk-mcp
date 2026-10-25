// Package audit 提供企业征信数据查询的审计日志能力。
//
// 业务背景：企业征信数据受监管，AppCode 维度的查询必须留痕（人行/工信部对查询留痕有要求）。
// 本包提供：
//   - 异步 JSON Lines 落盘（不阻塞业务请求）
//   - 隐私安全：ent_name/company_id 不直接落库，仅记录 hash + 长度
//   - HTTP 层 + MCP 工具层双层埋点（外层=AppCode+方法名+总耗时，内层=pcode+节状态）
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
)

// MethodGateway 是网关层审计记录的 method 标记：每次实际发出的网关调用一条。
//
// 与工具层（tools/call）记录的区别：聚合工具一次调用内部扇出多个接口，
// 工具层响应只含顶层 pcode_used，无法按接口归因；网关层在调用收口点落笔，
// 给出逐 pcode 的真实调用次数（计费定价依据）。
const MethodGateway = "gateway"

// Record 单条审计记录（JSON Lines）
type Record struct {
	TS          string         `json:"ts"`           // RFC3339Nano
	RequestID   string         `json:"request_id,omitempty"` // 关联 HTTP 层与工具层的唯一请求 ID
	AppCode     string         `json:"app_code"`     // 调用方 AppCode（脱敏：保留前缀 4 位）
	Method      string         `json:"method"`       // MCP 方法（tools/call, resources/read 等）
	Tool        string         `json:"tool,omitempty"`     // 工具名（如 enterprise_due_diligence_snapshot）
	Pcode       string         `json:"pcode,omitempty"`    // 实际调用的 pcode（历史编码本地拒绝，不会出现在此）
	PcodeRaw    string         `json:"pcode_raw,omitempty"` // 用户原始传入的 pcode
	SubjectHash string         `json:"subject_hash,omitempty"` // 查询主体 hash（sha256）
	SubjectKind string         `json:"subject_kind,omitempty"` // ent_name / company_id / mixed
	SubjectLen  int            `json:"subject_len,omitempty"`  // 主体字符串长度（用于审计，不暴露 PII）
	Status      string         `json:"status"`        // ok / error / validation / config
	HTTPStatus  int            `json:"http_status,omitempty"`
	LatencyMS   int64          `json:"latency_ms"`
	RespBytes   int            `json:"resp_bytes,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorMsg    string         `json:"error_msg,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	ResultDigest string        `json:"result_digest,omitempty"` // 业务结果摘要（可选，风控审计用）
}

// SetExtra 写入一条附加字段（按键合并，首次调用时初始化 map）。
func (r *Record) SetExtra(key string, v any) {
	if r.Extra == nil {
		r.Extra = make(map[string]any, 2)
	}
	r.Extra[key] = v
}

// Logger 异步审计日志记录器
type Logger struct {
	enc   *json.Encoder
	mu    sync.Mutex
	out   io.Writer
	closer io.Closer // 底层文件句柄（stderr 为 nil），Close 时关闭，避免句柄泄漏
	async bool
	ch    chan Record
	chMu  sync.RWMutex // 保护 Record 的 send 与 Close 的 close(ch) 竞态
	wg    sync.WaitGroup
	drop  atomic.Uint64 // 队列满/关闭后丢弃计数
	encErr atomic.Uint64 // 写盘失败计数（监管留痕，磁盘满时必须可观测）
	closed atomic.Bool
}

var (
	defaultLogger *Logger
	defaultOnce   sync.Once
)

// Default 返回全局默认 Logger
//
// KEEPRISK_AUDIT_PATH 为空时输出到 stderr；为 "-" 也输出到 stderr；其他值为文件路径。
// KEEPRISK_AUDIT_ASYNC=false 可关闭异步（用于测试/排障）。
func Default() *Logger {
	defaultOnce.Do(func() {
		defaultLogger = newLogger()
	})
	return defaultLogger
}

// New 显式创建 Logger（测试/多实例场景）
func New(w io.Writer, async bool) *Logger {
	l := &Logger{out: w, async: async, enc: json.NewEncoder(w)}
	if async {
		l.ch = make(chan Record, 1024)
		l.wg.Add(1)
		go l.worker()
	}
	return l
}

func newLogger() *Logger {
	path := strings.TrimSpace(os.Getenv("KEEPRISK_AUDIT_PATH"))
	var w io.Writer = os.Stderr
	var closer io.Closer
	if path != "" && path != "-" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit: open %s failed: %v, fallback to stderr\n", path, err)
		} else {
			w = f
			closer = f
		}
	}
	async := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("KEEPRISK_AUDIT_ASYNC"))); v == "false" || v == "0" {
		async = false
	}
	l := New(w, async)
	l.closer = closer
	return l
}

// Record 写入一条审计记录（异步、非阻塞）
func (l *Logger) Record(r Record) {
	if l == nil {
		return
	}
	if r.TS == "" {
		r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if l.async {
		l.chMu.RLock()
		defer l.chMu.RUnlock()
		if l.closed.Load() {
			l.drop.Add(1) // Close 后的记录直接丢弃并计数，绝不向已关闭 channel 发送
			return
		}
		select {
		case l.ch <- r:
		default:
			// 队列满则丢弃并计数（不允许阻塞业务请求）
			l.drop.Add(1)
		}
		return
	}
	l.write(r)
}

func (l *Logger) worker() {
	defer l.wg.Done()
	for r := range l.ch {
		l.write(r)
	}
}

func (l *Logger) write(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(r); err != nil {
		// 磁盘满等写盘失败必须可观测，不能静默丢失审计留痕
		l.encErr.Add(1)
		fmt.Fprintf(os.Stderr, "audit: encode record failed: %v\n", err)
	}
}

// Close 排空队列并关闭（用于优雅停机）
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	if l.async {
		if !l.closed.CompareAndSwap(false, true) {
			return nil
		}
		l.chMu.Lock()
		close(l.ch)
		l.chMu.Unlock()
		l.wg.Wait()
	}
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}

// Dropped 返回因队列满/关闭后丢弃的记录数
func (l *Logger) Dropped() uint64 { return l.drop.Load() }

// EncodeErrors 返回写盘失败的记录数
func (l *Logger) EncodeErrors() uint64 { return l.encErr.Load() }

// HashSubject 计算查询主体的稳定 hash（不暴露 PII）
func HashSubject(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(sum[:8]) // 64 bit 足够冲突避免
}

// MaskAppCode 脱敏 AppCode（保留前 4 位）
func MaskAppCode(ac string) string {
	if ac == "" {
		return ""
	}
	if len(ac) <= 4 {
		return "****"
	}
	return ac[:4] + "****"
}

// SubjectKind 推断主体类型
func SubjectKind(entName, companyID string) string {
	hasEnt := strings.TrimSpace(entName) != ""
	hasCID := strings.TrimSpace(companyID) != ""
	switch {
	case hasEnt && hasCID:
		return "mixed"
	case hasEnt:
		return "ent_name"
	case hasCID:
		return "company_id"
	default:
		return ""
	}
}

// FromContext 便捷：从 ctx 取出 AppCode 并写入 Record
func (l *Logger) FromContext(ctx context.Context) Record {
	appCode := auth.AppCodeFromContext(ctx)
	return Record{
		AppCode:   MaskAppCode(appCode),
		RequestID: RequestIDFromContext(ctx),
	}
}

// requestIDKey 是 context 中 request_id 的 key 类型
type requestIDKey struct{}

// WithRequestID 将 request_id 注入 context
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext 从 context 中取出 request_id
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// ─────────────────── 网关层审计上下文（工具名 / 调用计数） ───────────────────

// toolKey 是 context 中父工具名的 key 类型
type toolKey struct{}

// WithTool 将当前 MCP 工具名注入 context。
// 网关层审计记录据此把每次网关调用归属到具体工具（tool 字段）。
func WithTool(ctx context.Context, tool string) context.Context {
	return context.WithValue(ctx, toolKey{}, tool)
}

// ToolFromContext 取出当前工具名（未注入时返回空串）
func ToolFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(toolKey{}).(string); ok {
		return v
	}
	return ""
}

// gatewayCounter 请求级网关调用计数器。
// 由 MCP 工具层注入指针，网关客户端自增；工具层据此在审计记录中给出
// gateway_calls（实际发出的网关调用次数，即计费单位还原依据）。
type gatewayCounter struct{ n atomic.Int64 }

type gatewayCounterKey struct{}

// WithGatewayCounter 注入请求级网关调用计数器
func WithGatewayCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, gatewayCounterKey{}, &gatewayCounter{})
}

// IncGatewayCount 自增网关调用计数（未注入计数器时空操作）
func IncGatewayCount(ctx context.Context) {
	if c, ok := ctx.Value(gatewayCounterKey{}).(*gatewayCounter); ok {
		c.n.Add(1)
	}
}

// GatewayCountFromContext 返回网关调用计数；ok=false 表示该 ctx 未注入计数器
// （区分"未埋点"与"埋点后确为 0 次"，避免把缺失读成零成本）。
func GatewayCountFromContext(ctx context.Context) (int64, bool) {
	if c, ok := ctx.Value(gatewayCounterKey{}).(*gatewayCounter); ok {
		return c.n.Load(), true
	}
	return 0, false
}

// PerPcodeEnabled 返回是否记录网关层逐接口（pcode）审计明细。
//
// KEEPRISK_AUDIT_PER_PCODE 控制：默认开启；false/0 关闭（调试或日志量敏感场景）。
// 关闭时工具层记录的 gateway_calls 汇总仍在（成本总量不丢失），仅缺接口粒度分布。
func PerPcodeEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KEEPRISK_AUDIT_PER_PCODE")))
	return !(v == "false" || v == "0")
}
