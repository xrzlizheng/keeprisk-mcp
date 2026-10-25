// Package quota 提供 per-AppCode 的限流与配额能力。
//
// 业务背景：企业征信接口按次计费，单个 AppCode 若不限制可能 1 秒内调用数百次，
// 直接造成客户资金损失 + 网关压力。本包提供三层防护：
//   1. 日配额：每个 AppCode 每天调用上限（默认 5000 次/天）
//   2. 速率：每个 AppCode 每秒调用上限（默认 50 次/秒，token bucket）
//   3. 并发：每个 AppCode 同时 inflight 上限（默认 10，避免单租户占满信号量）
//
// 设计要点：
//   - 全部内存态（重启清零，仅用于防护滥用，不用于计费）
//   - token bucket 自驱（不依赖外部时钟）
//   - 超限返回明确 error（含限额、剩余、Retry-After 提示）
package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xrzlizheng/keeprisk-mcp/internal/auth"
)

// ErrQuotaExceeded 日配额耗尽
type ErrQuotaExceeded struct {
	AppCode string
	Limit   int
	Used    int
}

func (e *ErrQuotaExceeded) Error() string {
	// 对客文案不带 AppCode 明文（账户标识不外泄）；AppCode 字段保留供内部/审计使用
	return fmt.Sprintf("quota_exceeded: 当日调用 %d/%d 已耗尽。请明日重试或申请提额。", e.Used, e.Limit)
}

// ErrRateLimited 速率超限
type ErrRateLimited struct {
	AppCode string
	Limit   int
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("rate_limited: 速率超限（%d/秒）。请降低调用频率。", e.Limit)
}

// ErrConcurrentLimited 并发超限
type ErrConcurrentLimited struct {
	AppCode string
	Limit   int
}

func (e *ErrConcurrentLimited) Error() string {
	return fmt.Sprintf("concurrent_limited: 同时处理请求数 %d 超限。请稍后重试。", e.Limit)
}

// Config 配额配置
type Config struct {
	DailyLimit  int           // 每日调用上限；0 表示不限
	RatePerSec  int           // 每秒调用上限（token bucket 容量）；0 表示不限
	Burst       int           // token bucket 突发容量；<=0 取 RatePerSec
	Concurrent  int           // 每 AppCode 最大并发；0 表示不限
	MaxApps     int           // AppCode 状态 map 容量上限；<=0 取 10000（防伪造 AppCode 内存 DoS）
}

func DefaultConfig() Config {
	return Config{
		DailyLimit: envInt("KEEPRISK_QUOTA_DAILY", 5000),
		RatePerSec: envInt("KEEPRISK_RATE_PER_SEC", 50),
		Burst:      envInt("KEEPRISK_RATE_BURST", 0),
		Concurrent: envInt("KEEPRISK_CONCURRENT_PER_APP", 10),
		MaxApps:    envInt("KEEPRISK_QUOTA_MAX_APPS", 10000),
	}
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// appState 单 AppCode 状态
type appState struct {
	mu          sync.Mutex
	day         string              // YYYY-MM-DD，用于日切
	used        int                 // 今日已用
	tokens      float64             // 当前 token 数（浮点）
	lastRefill  time.Time           // 上次 token 补充时间
	lastSeen    time.Time           // 最近活跃时间（LRU 淘汰依据）
	inflightSem chan struct{}       // 并发信号量
}

type Manager struct {
	cfg Config

	mu    sync.RWMutex
	apps  map[string]*appState

	stop chan struct{}
	wg   sync.WaitGroup
}

// New 创建 Manager
func New(cfg Config) *Manager {
	if cfg.Burst <= 0 {
		cfg.Burst = cfg.RatePerSec
	}
	if cfg.MaxApps <= 0 {
		cfg.MaxApps = 10000
	}
	m := &Manager{
		cfg:  cfg,
		apps: make(map[string]*appState),
		stop: make(chan struct{}),
	}
	// 日切定时器（每小时检查一次）
	m.wg.Add(1)
	go m.dayRolloverLoop()
	return m
}

func (m *Manager) Close() {
	close(m.stop)
	m.wg.Wait()
}

func (m *Manager) dayRolloverLoop() {
	defer m.wg.Done()
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.maybeRollover(now)
		}
	}
}

func (m *Manager) maybeRollover(now time.Time) {
	today := now.Format("2006-01-02")
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.apps {
		s.mu.Lock()
		if s.day != today {
			s.day = today
			s.used = 0
		}
		s.mu.Unlock()
	}
}

func (m *Manager) getState(appCode string) *appState {
	m.mu.RLock()
	s, ok := m.apps[appCode]
	m.mu.RUnlock()
	if ok {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.apps[appCode]; ok {
		return s
	}
	// 容量上限：伪造随机 AppCode 不会让 map 无界增长；满时淘汰最久未活跃条目
	if len(m.apps) >= m.cfg.MaxApps {
		var oldestKey string
		var oldestTime time.Time
		for k, v := range m.apps {
			if oldestKey == "" || v.lastSeen.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.lastSeen
			}
		}
		delete(m.apps, oldestKey)
	}
	now := time.Now()
	s = &appState{
		day:         now.Format("2006-01-02"),
		tokens:      float64(m.cfg.Burst),
		lastRefill:  now,
		lastSeen:    now,
		inflightSem: make(chan struct{}, m.cfg.Concurrent),
	}
	m.apps[appCode] = s
	return s
}

// lookupState 只读查询：不创建条目（Status/Release 使用，避免探测类调用撑大 map）
func (m *Manager) lookupState(appCode string) *appState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.apps[appCode]
}

// Acquire 申请一次调用许可
//
// 返回：ctx 取消时返回 ctx.Err()；超限返回对应 Err* 类型。
// 调用方在 defer Release 中释放并发信号量。
//
// 扣费顺序设计（修复历史 bug）：
//   - 日配额计数只在"全部闸门通过"之后才 +1——被限流/取消的请求不产生网关调用，绝不消耗日配额
//   - 并发闸门为排队等待（受 ctx 控制）而非 fail-fast——聚合工具内部扇出
//     （如 compare_enterprises 峰值 > Concurrent）不再出现大面积节失败
func (m *Manager) Acquire(ctx context.Context, appCode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.getState(appCode)
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()

	// 1. 日配额预检（不扣减；真正扣减在全部闸门通过后）
	if m.cfg.DailyLimit > 0 {
		if err := m.checkDaily(s, appCode); err != nil {
			return err
		}
	}

	// 2. 速率（token bucket）
	if m.cfg.RatePerSec > 0 {
		// 等到拿到一个 token 或 ctx 取消
		deadline, hasDeadline := ctx.Deadline()
		for {
			s.mu.Lock()
			now := time.Now()
			elapsed := now.Sub(s.lastRefill).Seconds()
			s.tokens += elapsed * float64(m.cfg.RatePerSec)
			if s.tokens > float64(m.cfg.Burst) {
				s.tokens = float64(m.cfg.Burst)
			}
			s.lastRefill = now
			ok := s.tokens >= 1.0
			if ok {
				s.tokens -= 1.0
			}
			s.mu.Unlock()
			if ok {
				break
			}
			// 计算需要等待的时间（到下一个 token）
			waitDur := time.Duration(float64(time.Second) / float64(m.cfg.RatePerSec))
			if hasDeadline && time.Until(deadline) < waitDur {
				return &ErrRateLimited{AppCode: appCode, Limit: m.cfg.RatePerSec}
			}
			t := time.NewTimer(waitDur)
			select {
			case <-t.C:
				// 继续尝试
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		}
	}

	// 3. 并发信号量：排队等待空位（ctx 可取消），不再 fail-fast
	if m.cfg.Concurrent > 0 {
		select {
		case s.inflightSem <- struct{}{}:
			// acquired
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 4. 全部闸门通过：此时才计日配额（复检 + 扣减原子完成）
	if m.cfg.DailyLimit > 0 {
		s.mu.Lock()
		today := time.Now().Format("2006-01-02")
		if s.day != today {
			s.day = today
			s.used = 0
		}
		if s.used >= m.cfg.DailyLimit {
			s.mu.Unlock()
			// 极端并发下配额在排队期间耗尽：释放已获取的并发信号量再报错
			if m.cfg.Concurrent > 0 {
				select {
				case <-s.inflightSem:
				default:
				}
			}
			return &ErrQuotaExceeded{AppCode: appCode, Limit: m.cfg.DailyLimit, Used: s.used}
		}
		s.used++
		s.mu.Unlock()
	}
	return nil
}

// checkDaily 日配额只读预检（不扣减）
func (m *Manager) checkDaily(s *appState, appCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if s.day != today {
		s.day = today
		s.used = 0
	}
	if s.used >= m.cfg.DailyLimit {
		return &ErrQuotaExceeded{AppCode: appCode, Limit: m.cfg.DailyLimit, Used: s.used}
	}
	return nil
}

// Release 释放并发信号量（仅在 Acquire 时使用了并发限制时调用）
func (m *Manager) Release(appCode string) {
	if m.cfg.Concurrent <= 0 {
		return
	}
	s := m.lookupState(appCode)
	if s == nil {
		return // 状态已被 LRU 淘汰，无需释放（信号量随状态一起被丢弃）
	}
	select {
	case <-s.inflightSem:
	default:
		// 不会发生
	}
}

// Status 返回某 AppCode 当前用量快照（用于 list_data_capabilities 等查询）
type Status struct {
	AppCode   string `json:"app_code"`
	DayUsed   int    `json:"day_used"`
	DayLimit  int    `json:"day_limit"`
	Tokens    int    `json:"tokens"`
	Inflight  int    `json:"inflight"`
	LimitHit  bool   `json:"day_quota_reached"`
}

func (m *Manager) Status(appCode string) Status {
	s := m.lookupState(appCode)
	if s == nil {
		// 只读查询不为未知 AppCode 创建状态（防探测撑大 map）
		return Status{AppCode: appCode, DayLimit: m.cfg.DailyLimit, Tokens: m.cfg.Burst}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inflight := len(s.inflightSem)
	return Status{
		AppCode:  appCode,
		DayUsed:  s.used,
		DayLimit: m.cfg.DailyLimit,
		Tokens:   int(s.tokens),
		Inflight: inflight,
		LimitHit: m.cfg.DailyLimit > 0 && s.used >= m.cfg.DailyLimit,
	}
}

// AllStatus 全部 AppCode 状态（用于运维/审计）
func (m *Manager) AllStatus() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.apps))
	for ac := range m.apps {
		out = append(out, m.Status(ac))
	}
	return out
}

// IsLimitError 判断是否为配额/限流错误（便于上层路由返回 429）
func IsLimitError(err error) bool {
	var qe *ErrQuotaExceeded
	var re *ErrRateLimited
	var ce *ErrConcurrentLimited
	return errors.As(err, &qe) || errors.As(err, &re) || errors.As(err, &ce)
}

// FromContext 便捷：从 ctx 取出 AppCode 并 Acquire
func (m *Manager) AcquireFromContext(ctx context.Context) (string, error) {
	ac := auth.AppCodeFromContext(ctx)
	if ac == "" {
		return "", fmt.Errorf("quota: 服务端鉴权上下文缺失，请稍后重试")
	}
	return ac, m.Acquire(ctx, ac)
}
