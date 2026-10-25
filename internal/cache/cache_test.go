package cache

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLRUEviction(t *testing.T) {
	// MaxSize=3，写入 4 个 key 后最旧的应被淘汰
	cfg := Config{Enabled: true, TTL: 10 * time.Second, MaxSize: 3}
	c := New(cfg)

	for i, k := range []string{"a", "b", "c", "d"} {
		val, _ := json.Marshal(map[string]any{"k": k})
		c.Set(k, val)
		_ = i
	}

	// "a" 应被淘汰
	if _, hit := c.Get("a"); hit {
		t.Fatal("expected key 'a' to be evicted as LRU victim")
	}
	// "b","c","d" 应存在
	for _, k := range []string{"b", "c", "d"} {
		if _, hit := c.Get(k); !hit {
			t.Fatalf("expected key %q to exist", k)
		}
	}
}

func TestLRUTouchOnGet(t *testing.T) {
	// MaxSize=2，Get("a") 后再 Set("c")，"b" 应被淘汰而非 "a"
	cfg := Config{Enabled: true, TTL: 10 * time.Second, MaxSize: 2}
	c := New(cfg)

	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))

	// 触发 "a" 的 LRU 更新
	if _, hit := c.Get("a"); !hit {
		t.Fatal("expected key 'a' to exist")
	}

	// 写入 "c" 应淘汰 "b"
	c.Set("c", []byte("3"))

	if _, hit := c.Get("b"); hit {
		t.Fatal("expected key 'b' to be evicted (not 'a')")
	}
	if _, hit := c.Get("a"); !hit {
		t.Fatal("expected key 'a' to still exist after touch")
	}
	if _, hit := c.Get("c"); !hit {
		t.Fatal("expected key 'c' to exist")
	}
}

func TestTTLExpiry(t *testing.T) {
	cfg := Config{Enabled: true, TTL: 50 * time.Millisecond, MaxSize: 10}
	c := New(cfg)

	c.Set("k", []byte("v"))
	if _, hit := c.Get("k"); !hit {
		t.Fatal("expected hit before TTL expiry")
	}

	time.Sleep(60 * time.Millisecond)
	if _, hit := c.Get("k"); hit {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestStats(t *testing.T) {
	cfg := Config{Enabled: true, TTL: 10 * time.Second, MaxSize: 5}
	c := New(cfg)

	c.Set("a", []byte("1"))
	c.Get("a") // hit
	c.Get("missing") // miss

	s := c.Stats()
	if s.Size != 1 {
		t.Fatalf("expected size=1, got %d", s.Size)
	}
	if s.Hits != 1 {
		t.Fatalf("expected hits=1, got %d", s.Hits)
	}
	if s.Misses != 1 {
		t.Fatalf("expected misses=1, got %d", s.Misses)
	}
}

func TestSetWithTTL(t *testing.T) {
	cfg := Config{Enabled: true, TTL: 10 * time.Second, MaxSize: 5}
	c := New(cfg)

	// 自定义短 TTL
	c.SetWithTTL("k", []byte("v"), 50*time.Millisecond)
	if _, hit := c.Get("k"); !hit {
		t.Fatal("expected hit before custom TTL expiry")
	}

	time.Sleep(60 * time.Millisecond)
	if _, hit := c.Get("k"); hit {
		t.Fatal("expected miss after custom TTL expiry")
	}
}

func TestDisabledCache(t *testing.T) {
	cfg := Config{Enabled: false, TTL: 0, MaxSize: 5}
	c := New(cfg)

	c.Set("a", []byte("1"))
	if _, hit := c.Get("a"); hit {
		t.Fatal("expected miss when cache disabled")
	}
}

// 回归（P0 跨租户读旁路）：缓存默认必须按 AppCode 隔离；
// 仅显式 KEEPRISK_CACHE_PER_APPCODE=false 才关闭隔离。
func TestDefaultConfigPerAppCode(t *testing.T) {
	t.Setenv("KEEPRISK_CACHE_PER_APPCODE", "")
	cfg := DefaultConfig()
	if !cfg.PerAppCode {
		t.Fatal("缓存默认必须按 AppCode 隔离（防伪造 AppCode 跨租户读旁路）")
	}

	t.Setenv("KEEPRISK_CACHE_PER_APPCODE", "false")
	if DefaultConfig().PerAppCode {
		t.Fatal("显式 KEEPRISK_CACHE_PER_APPCODE=false 应关闭隔离")
	}

	t.Setenv("KEEPRISK_CACHE_PER_APPCODE", "true")
	if !DefaultConfig().PerAppCode {
		t.Fatal("显式 KEEPRISK_CACHE_PER_APPCODE=true 应开启隔离")
	}
}

// 隔离开启时，不同 AppCode 写入的条目互相不可见。
func TestPerAppCodeKeyIsolation(t *testing.T) {
	cfg := Config{Enabled: true, TTL: 10 * time.Second, MaxSize: 10, PerAppCode: true}
	c := New(cfg)

	ka := KeyWithAppCode("E_007_0501", map[string]any{"entName": "A公司"}, "RealApp")
	kb := KeyWithAppCode("E_007_0501", map[string]any{"entName": "A公司"}, "FakeApp")
	if ka == kb {
		t.Fatal("不同 AppCode 的缓存 key 必须不同")
	}
	c.Set(ka, []byte(`{"secret":true}`))
	if _, hit := c.Get(kb); hit {
		t.Fatal("FakeApp 不得命中 RealApp 写入的缓存条目（跨租户读旁路）")
	}
	if _, hit := c.Get(ka); !hit {
		t.Fatal("RealApp 应命中自己的缓存条目")
	}
}
