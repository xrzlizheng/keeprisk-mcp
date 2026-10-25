package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xrzlizheng/keeprisk-mcp/internal/audit"
	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
	mcpsrv "github.com/xrzlizheng/keeprisk-mcp/internal/mcp"
)

func main() {
	// 入站鉴权：Authorization token → AppCode 映射（配置文件热更优先，env 引导兜底）。
	// 解析失败 fail-fast，避免生产配置事故静默为全量 401。
	resolver, err := auth.NewMapResolver()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	log.Printf("auth: %s loaded, %d token(s)", resolver.Source(), resolver.Len())

	// 热更新支持：监听 SIGHUP 信号——二进制有更新则 exec 热替换；
	// 否则热更新授权映射（进程内原子换表，连接与会话保持不断）。
	if err := handleHotReload(resolver); err != nil {
		log.Printf("hot reload handler error: %v", err)
	}

	runServer(resolver)
}

// handleHotReload 监听 SIGHUP 信号，按变更内容二选一：
//   - 磁盘二进制较启动时已更新 → syscall.Exec 热替换进程（会话中断，与既有行为一致）；
//   - 二进制未变且配置了 auth.EnvAuthMapFileKey → 进程内热更新授权映射，
//     新增/吊销 token 无需重启容器（exec 会打断所有会话，二进制没变时不该付出这个代价）。
func handleHotReload(resolver *auth.MapResolver) error {
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)

	// 启动期确定热更二进制路径并记录 ModTime 作为基准（go build 每次产出新 mtime）。
	// 优先目录挂载（docker-compose: ./build:/app/bin）——单文件 bind-mount
	// 在 go build rename（新 inode）后容器仍绑定旧 inode，曾导致热更新
	// 静默加载旧代码且健康检查无法发现；目录挂载总是解析目录内最新文件。
	// 旧单文件挂载路径 /keeprisk-mcp 保留兼容；均不存在时保持当前可执行文件。
	binPath, err := os.Executable()
	if err != nil {
		log.Printf("hot reload: cannot get executable path: %v", err)
	}
	for _, candidate := range []string{"/app/bin/keeprisk-mcp", "/keeprisk-mcp"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode()&0111 != 0 {
			binPath = candidate
			break
		}
	}
	var startModTime time.Time
	if info, err := os.Stat(binPath); err == nil {
		startModTime = info.ModTime()
	}

	go func() {
		for range sighupCh {
			log.Println("received SIGHUP signal, reloading...")
			if info, err := os.Stat(binPath); err == nil && !info.ModTime().Equal(startModTime) {
				log.Printf("hot reload: binary updated, exec-restarting with %s", binPath)
				// 使用 syscall.Exec 替换当前进程（真正的热替换）
				if err := syscall.Exec(binPath, os.Args, os.Environ()); err != nil {
					log.Printf("hot reload: exec failed: %v", err)
				}
				continue
			}
			if path := strings.TrimSpace(os.Getenv(auth.EnvAuthMapFileKey)); path != "" {
				n, err := resolver.ReloadFromFile(path)
				if err != nil {
					log.Printf("hot reload: auth map NOT updated, keeping previous %d token(s): %v", n, err)
					continue
				}
				log.Printf("hot reload: auth map reloaded in place, %d token(s), sessions preserved", n)
				continue
			}
			log.Printf("hot reload: binary unchanged and %s not configured, nothing to do", auth.EnvAuthMapFileKey)
		}
	}()
	return nil
}

func runServer(resolver *auth.MapResolver) {
	svc, err := mcpsrv.MustServices()
	if err != nil {
		log.Fatal(err)
	}
	server := mcpsrv.NewServer(svc)

	addr := strings.TrimSpace(os.Getenv("MCP_HTTP_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	mcpPath := strings.TrimSpace(os.Getenv("MCP_HTTP_PATH"))
	if mcpPath == "" {
		mcpPath = "/mcp"
	}

	public := http.NewServeMux()
	public.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	public.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 检查 catalog 是否加载成功、网关客户端是否初始化成功
		ready := svc.Cat != nil && svc.Cat.LenLatest() > 0 && svc.KR != nil
		status := 200
		apis := 0
		if svc.Cat != nil { // nil 防御覆盖到取值处，避免 panic
			apis = svc.Cat.LenLatest()
		}
		out := map[string]any{
			"ready":     ready,
			"apis":      apis,
			"cache":     svc.Cache.Stats(),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		if !ready {
			status = 503
			out["reason"] = "catalog empty or client not initialized"
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	})

	protected := http.NewServeMux()
	// TODO: 当 go-sdk 支持 StreamableHTTPOptions 时，设置 SSE 超时为 120s
	// handler := mcp.NewStreamableHTTPHandler(..., &mcp.StreamableHTTPOptions{SSETimeout: 120 * time.Second})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	protected.Handle(mcpPath, handler)
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("KeepRisk Enterprise MCP — POST " + mcpPath + "，Header: Authorization: <token>（兼容 Bearer 前缀）\n"))
	})

	// 组合中间件：审计（外层，捕获所有请求包括 401）+ 鉴权（内层）
	chain := func(h http.Handler) http.Handler {
		return auditHTTPMiddleware(auth.Middleware(resolver, h))
	}

	root := http.NewServeMux()
	root.Handle("/health", public)
	root.Handle("/ready", public)
	root.Handle("/", chain(protected))

	// 用 http.Server 替代 ListenAndServe 以支持优雅停机
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		// WriteTimeout:    120 * time.Second, // 注释掉 WriteTimeout 以支持 MCP 的长连接 (SSE)
		IdleTimeout:       90 * time.Second,
	}

	// 信号监听：SIGINT/SIGTERM 触发优雅停机
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("keeprisk-mcp listen %s mcp=%s (Authorization required except /health, /ready)", addr, mcpPath)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-stop
	log.Println("shutdown signal received, draining...")

	// 给 inflight 请求最多 30 秒优雅退出
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}

	// 排空审计日志 + 配额后台任务
	if err := audit.Default().Close(); err != nil {
		log.Printf("audit close: %v", err)
	}
	svc.Quota.Close()
	log.Printf("audit dropped: %d (queue full)", audit.Default().Dropped())
	log.Println("shutdown complete")
}

// auditHTTPMiddleware 记录每次受保护 HTTP 请求的审计日志（外层信封：状态/总耗时/AppCode）
//
// 设计要点：外层中间件读不到内层 auth 写入 ctx 的值，鉴权结论经 outcomeSink 载体
// （statusRecorder.SetAuthOutcome）回传。成功请求记映射后 AppCode 的脱敏值；
// 401 失败请求仅记 token 指纹（sha256 前 12 位）+ 失败类别，完整 Authorization 绝不落盘。
// 生成唯一 request_id 注入 context，使 HTTP 层与工具层审计可关联。
func auditHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// 生成 request_id（用于关联 HTTP 层与工具层审计日志）
		reqID := generateRequestID()
		ctx := audit.WithRequestID(r.Context(), reqID)
		r = r.WithContext(ctx)

		rw := &statusRecorder{ResponseWriter: w, status: 200}
		// 将 request_id 写入响应头，便于客户端排障
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(rw, r)

		rec := audit.Record{
			Method:     "http",
			RequestID:  reqID,
			Tool:       r.URL.Path,
			HTTPStatus: rw.status,
			LatencyMS:  time.Since(start).Milliseconds(),
			RespBytes:  rw.bytes,
		}
		if rw.outcome.AppCode != "" {
			rec.AppCode = audit.MaskAppCode(rw.outcome.AppCode)
		}
		if rec.AppCode == "" {
			rec.AppCode = "-"
		}
		if rw.outcome.Failure != "" {
			rec.Extra = map[string]any{"auth_failure": rw.outcome.Failure}
			if rw.outcome.TokenFP != "" {
				rec.Extra["token_fp"] = rw.outcome.TokenFP
			}
		}
		switch {
		case rw.status >= 500:
			rec.Status = "error"
		case rw.status == 401:
			rec.Status = "unauthorized"
		case rw.status >= 400:
			rec.Status = "client_error"
		default:
			rec.Status = "ok"
		}
		audit.Default().Record(rec)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
	outcome     auth.AuthOutcome
}

// SetAuthOutcome 实现 auth.outcomeSink：内层鉴权结论经此回传外层审计。
func (s *statusRecorder) SetAuthOutcome(o auth.AuthOutcome) { s.outcome = o }

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush 支持 SSE (Server-Sent Events) 需要的 Flusher 接口
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 支持 Go 1.20+ 的 http.ResponseController 获取底层 ResponseWriter
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// generateRequestID 生成 16 字节随机 hex 编码的 request_id
func generateRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}