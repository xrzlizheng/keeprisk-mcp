// Package cache 提供网关响应的内存缓存层。
//
// 业务背景：企业征信接口按次计费。同一企业同一接口的查询结果在短时间内基本不变
// （工商基本信息一天内基本不变，被执行/失信近实时）。在 MCP 层加缓存可：
//   - 显著降低客户成本（5 分钟内同 pcode+params 直接命中，不再计费）
//   - 缓解网关压力
//
// 设计要点：
//   - 默认按 AppCode 隔离（安全默认；全局共享缓存曾存在跨租户读旁路 P0）
//   - LRU + TTL 混合淘汰
//   - 默认关闭（KEEPRISK_CACHE_TTL_SEC=0）；启用建议 60-300 秒
//   - 仅缓存网关成功响应（code=100/110），错误不缓存
//   - 命中时返回 _cache_hit=true 元数据，调用方可观察缓存效果
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 缓存配置
type Config struct {
	Enabled      bool          // 是否启用（由 TTL > 0 推断）
	TTL          time.Duration // 默认 TTL
	MaxSize      int           // 最大条目数
	PerAppCode   bool          // 是否按 AppCode 隔离缓存（合规优先，默认 false）
}

func DefaultConfig() Config {
	ttlSec := envInt("KEEPRISK_CACHE_TTL_SEC", 0)
	max := envInt("KEEPRISK_CACHE_MAX", 1000)
	// 安全默认：缓存按 AppCode 隔离。曾默认全局共享（key 不含 AppCode），
	// 实测伪造合法格式的 AppCode 即可在网关鉴权前命中其他租户已付费查询的
	// 数据（跨租户缓存读旁路，P0）。仅显式 KEEPRISK_CACHE_PER_APPCODE=false
	// 才关闭隔离（成本优先场景自行承担该风险）。
	perApp := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("KEEPRISK_CACHE_PER_APPCODE"))); v == "false" || v == "0" {
		perApp = false
	}
	return Config{
		Enabled:    ttlSec > 0,
		TTL:        time.Duration(ttlSec) * time.Second,
		MaxSize:    max,
		PerAppCode: perApp,
	}
}

// CacheHints 按 pcode 前缀定义推荐 TTL。
// 不同企业数据变化频率差异极大：工商基本信息数月不变，被执行/失信可能每日更新，舆情实时变化。
var CacheHints = map[string]time.Duration{
	"E_007_05": 3600 * time.Second,  // 工商基本信息（极低变化）
	"E_007_09": 1800 * time.Second,  // 股东信息
	"E_007_06": 1800 * time.Second,  // 主要人员/股权穿透
	"E_007_02": 1800 * time.Second,  // 实际控制权
	"E_007_03": 1800 * time.Second,  // 最终受益人
	"E_009_":   0,                    // 舆情类（不缓存）
	"E_003_07": 60 * time.Second,    // 被执行人（高变化）
	"E_003_08": 60 * time.Second,    // 失信人（高变化）
	"P_003_":   60 * time.Second,    // 限高（高变化）
	"E_003_04": 120 * time.Second,   // 裁判文书/严重失信
	"E_003_03": 120 * time.Second,   // 立案/终本
	"E_003_06": 120 * time.Second,   // 法院公告
	"E_003_09": 120 * time.Second,   // 开庭公告
	"E_003_15": 120 * time.Second,   // 司法拍卖
	"E_003_18": 300 * time.Second,   // 破产重整
	"E_007_10": 300 * time.Second,   // 行政处罚
	"E_007_12": 300 * time.Second,   // 经营异常
	"E_007_14": 300 * time.Second,   // 严重违法
	"E_008_":   300 * time.Second,   // 税务类
	"E_011_04": 300 * time.Second,   // 税务评级/产品质量
	"E_011_03": 300 * time.Second,   // 供应商与客户
}

// HintTTL 返回指定 pcode 的推荐缓存 TTL。
// 匹配最长前缀；无匹配返回 defaultTTL。
func HintTTL(pcode string, defaultTTL time.Duration) time.Duration {
	bestLen := 0
	var bestTTL time.Duration
	found := false
	for prefix, ttl := range CacheHints {
		if strings.HasPrefix(pcode, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			bestTTL = ttl
			found = true
		}
	}
	if found {
		return bestTTL
	}
	return defaultTTL
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

// entry 缓存条目
type entry struct {
	key       string
	value     []byte // 序列化后的 map[string]any（紧凑 JSON）
	expiresAt time.Time
	hit       int // 命中次数（用于观察）
	elem      *list.Element // 在 lru 中的位置，O(1) 移动/删除
}

// Cache 内存缓存
type Cache struct {
	cfg Config

	mu      sync.RWMutex
	entries map[string]*entry
	lru     *list.List // LRU 顺序：_front 为最久未使用，back 为最近使用

	hits    uint64
	misses  uint64
	evicted uint64
}

func New(cfg Config) *Cache {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 1000
	}
	return &Cache{
		cfg:     cfg,
		entries: make(map[string]*entry, cfg.MaxSize),
		lru:     list.New(),
	}
}

// IsPerAppCode 返回是否启用了按 AppCode 隔离缓存
func (c *Cache) IsPerAppCode() bool {
	return c.cfg.PerAppCode
}

// TTL 返回全局默认 TTL（供 per-pcode HintTTL 兜底使用）
func (c *Cache) TTL() time.Duration {
	return c.cfg.TTL
}

// Key 计算缓存 key：pcode + 规范化参数 hash
// 如果启用了 PerAppCode，调用方应通过 KeyWithAppCode 传入 appCode
func Key(pcode string, params map[string]any) string {
	return KeyWithAppCode(pcode, params, "")
}

// KeyWithAppCode 计算缓存 key，可选包含 AppCode 用于租户隔离
func KeyWithAppCode(pcode string, params map[string]any, appCode string) string {
	// 参数序列化：按 key 排序后拼接，确保同语义参数生成同 key
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	if appCode != "" {
		sb.WriteString(appCode)
		sb.WriteString("|")
	}
	sb.WriteString(pcode)
	sb.WriteString("|")
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(toString(params[k]))
		sb.WriteString("&")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	prefix := pcode
	if appCode != "" {
		prefix = appCode[:min(4, len(appCode))] + ":" + pcode
	}
	return prefix + ":" + hex.EncodeToString(sum[:12])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// Get 取缓存（命中返回 value + true）
func (c *Cache) Get(key string) ([]byte, bool) {
	if !c.cfg.Enabled {
		return nil, false
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	if !ok {
		c.mu.RUnlock()
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		c.mu.RUnlock()
		c.mu.Lock()
		c.evictEntry(e)
		c.misses++
		c.mu.Unlock()
		return nil, false
	}
	val := e.value
	c.mu.RUnlock()
	c.mu.Lock()
	e.hit++ // 写操作必须在写锁内（RWMutex 允许多读者并存）
	c.hits++
	c.touchLRU(e)
	c.mu.Unlock()
	return val, true
}

// Set 写缓存
func (c *Cache) Set(key string, value []byte) {
	if !c.cfg.Enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, exists := c.entries[key]; exists {
		e.value = value
		e.expiresAt = time.Now().Add(c.cfg.TTL)
		c.touchLRU(e)
		return
	}
	if len(c.entries) >= c.cfg.MaxSize {
		// 淘汰最久未使用（lru 前端）
		if oldElem := c.lru.Front(); oldElem != nil {
			oldEntry := oldElem.Value.(*entry)
			c.evictEntry(oldEntry)
			c.evicted++
		}
	}
	e := &entry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(c.cfg.TTL),
	}
	e.elem = c.lru.PushBack(e)
	c.entries[key] = e
}

// SetWithTTL 写缓存，使用自定义 TTL 而非默认值。
// 用于 per-pcode 差异化缓存策略。
func (c *Cache) SetWithTTL(key string, value []byte, ttl time.Duration) {
	if !c.cfg.Enabled || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, exists := c.entries[key]; exists {
		e.value = value
		e.expiresAt = time.Now().Add(ttl)
		c.touchLRU(e)
		return
	}
	if len(c.entries) >= c.cfg.MaxSize {
		if oldElem := c.lru.Front(); oldElem != nil {
			oldEntry := oldElem.Value.(*entry)
			c.evictEntry(oldEntry)
			c.evicted++
		}
	}
	e := &entry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
	e.elem = c.lru.PushBack(e)
	c.entries[key] = e
}

// touchLRU 将 entry 移到 lru 末尾（最近使用），O(1)
func (c *Cache) touchLRU(e *entry) {
	if e.elem != nil {
		c.lru.MoveToBack(e.elem)
	}
}

// evictEntry 从 entries 和 lru 中同步删除条目，O(1)
func (c *Cache) evictEntry(e *entry) {
	if e.elem != nil {
		c.lru.Remove(e.elem)
		e.elem = nil
	}
	delete(c.entries, e.key)
}

// Stats 缓存统计（用于 list_data_capabilities 输出）
type Stats struct {
	Enabled    bool   `json:"enabled"`
	Size       int    `json:"size"`
	MaxSize    int    `json:"max_size"`
	TTLSec     int    `json:"ttl_sec"`
	PerAppCode bool   `json:"per_app_code"`
	Hits       uint64 `json:"hits"`
	Misses     uint64 `json:"misses"`
	Evicted    uint64 `json:"evicted"`
	HitRatio   string `json:"hit_ratio"`
}

func (c *Cache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	total := c.hits + c.misses
	var ratio string
	if total > 0 {
		ratio = strconv.FormatFloat(float64(c.hits)/float64(total), 'f', 3, 64)
	} else {
		ratio = "0"
	}
	return Stats{
		Enabled:    c.cfg.Enabled,
		Size:       len(c.entries),
		MaxSize:    c.cfg.MaxSize,
		TTLSec:     int(c.cfg.TTL.Seconds()),
		PerAppCode: c.cfg.PerAppCode,
		Hits:       c.hits,
		Misses:     c.misses,
		Evicted:    c.evicted,
		HitRatio:   ratio,
	}
}
