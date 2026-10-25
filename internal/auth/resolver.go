package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
)

// AppCodeResolver 将入站凭证（Authorization token）解析为 KeepRisk AppCode。
//
// 现阶段实现为 MapResolver（映射组暂存 env/文件，过渡方案）；
// 未来接口 A（Authorization→AppCode 查询服务）上线后替换为 HTTPResolver，
// Middleware 与下游（keeprisk client/quota/cache/audit）均不感知变化。
type AppCodeResolver interface {
	Resolve(ctx context.Context, token string) (appCode string, ok bool)
}

// envAuthMapKey 承载 token→AppCode 映射组（未配置文件时的引导来源）。支持两种格式
// （先按 JSON 解析，失败再按简写解析，均失败则启动期报错 fail-fast，
// 避免生产配置事故静默为全量 401）：
//
//	JSON 对象（推荐，token 可含特殊字符）：{"tok_alice":"AppCodeAlice","tok_bob":"AppCodeBob"}
//	简写（AppCode 字符集不含冒号，按首个冒号切分）：tok_alice:AppCodeAlice,tok_bob:AppCodeBob
//
// 安全边界：env 明文映射属过渡方案，生产应通过 secrets/配置中心注入，
// 映射值勿写入镜像层与版本库。
const envAuthMapKey = "KEEPRISK_AUTH_MAP"

// EnvAuthMapFileKey 指向映射组文件（热更新来源）。配置后文件为唯一来源：
// 启动从文件加载（缺失/非法即 fail-fast，不静默退回 env——退回会让已吊销
// token 继续有效）；SIGHUP 触发进程内原子换表，授权增删无需重启容器。
// 未配置时退回 env 引导（env 无法从进程外修改，故 env 模式不支持热更新）。
//
// 文件内容与 env 同格式（JSON 对象或 token:appcode 逗号简写）。
// 安全边界：文件明文映射属过渡方案，生产应通过 secrets/配置中心注入，
// 映射值勿写入镜像层与版本库（仓库已将 config/ 加入 .gitignore）。
const EnvAuthMapFileKey = "KEEPRISK_AUTH_MAP_FILE"

// authMapFileMaxBytes 映射组文件大小上限（防误挂大文件的内存 DoS 面）。
const authMapFileMaxBytes = 1 << 20

// MapResolver 基于内存映射表的凭证解析器，支持 SIGHUP 原子热更新。
// 只查表不插入：伪造随机 token 不会造成内存增长（map 容量固定为映射组数）。
// 映射表经 atomic.Pointer 整表换入换出，读侧无锁；Reload 先解析校验再换表，
// 非法变更不会清空既有映射（避免热更事故静默放大为全量 401）。
type MapResolver struct {
	m      atomic.Pointer[map[string]string]
	source string // 加载来源描述（env 键名或文件路径），构造后只读，供日志/排障
}

// NewMapResolver 构造解析器：配置了 EnvAuthMapFileKey 时以文件为唯一来源，
// 否则从 env 加载；两者缺失/非法均 fail-fast。
func NewMapResolver() (*MapResolver, error) {
	if path := strings.TrimSpace(os.Getenv(EnvAuthMapFileKey)); path != "" {
		return NewMapResolverFromFile(path)
	}
	return NewMapResolverFromString(os.Getenv(envAuthMapKey))
}

// NewMapResolverFromFile 从文件加载映射表（也是 SIGHUP 热更新的重载路径）。
func NewMapResolverFromFile(path string) (*MapResolver, error) {
	spec, err := readAuthMapFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s 加载失败: %w", path, err)
	}
	return newMapResolver(spec, path)
}

// NewMapResolverFromString 从字符串加载映射表（env 引导/测试/多实例用）。
func NewMapResolverFromString(spec string) (*MapResolver, error) {
	return newMapResolver(spec, envAuthMapKey)
}

func newMapResolver(spec, source string) (*MapResolver, error) {
	m, err := buildAuthMap(spec)
	if err != nil {
		return nil, fmt.Errorf("%s 加载失败: %w", source, err)
	}
	r := &MapResolver{source: source}
	r.m.Store(&m)
	return r, nil
}

// readAuthMapFile 读取映射组文件（上限 1MB，空白文件视为配置错误）。
func readAuthMapFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开映射文件失败: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, authMapFileMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取映射文件失败: %w", err)
	}
	if len(raw) > authMapFileMaxBytes {
		return "", fmt.Errorf("映射文件超过 %d 字节上限", authMapFileMaxBytes)
	}
	spec := strings.TrimSpace(string(raw))
	if spec == "" {
		return "", fmt.Errorf("映射文件为空")
	}
	return spec, nil
}

// buildAuthMap 解析并校验映射组（JSON 对象与 token:appcode 逗号简写两种格式）。
func buildAuthMap(spec string) (map[string]string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("未配置映射：Authorization 鉴权需要至少一组 token→AppCode 映射")
	}
	m, err := parseAuthMap(spec)
	if err != nil {
		return nil, err
	}
	for token, appCode := range m {
		if token == "" {
			return nil, fmt.Errorf("存在空 token 条目")
		}
		// 复核映射值格式：脏数据不得进入 quota/cache 状态 map（内存 DoS 面）
		if !ValidAppCode(appCode) {
			return nil, fmt.Errorf("token %s 映射的 AppCode 格式非法（仅允许 1-64 位字母/数字/下划线/连字符）",
				TokenFingerprint(token))
		}
	}
	return m, nil
}

// parseAuthMap 依次尝试 JSON 对象与 token:appcode 逗号简写两种格式。
func parseAuthMap(spec string) (map[string]string, error) {
	var viaJSON map[string]string
	if err := json.Unmarshal([]byte(spec), &viaJSON); err == nil && viaJSON != nil {
		return viaJSON, nil
	}
	m := make(map[string]string)
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			// 报错不回显条目内容（含凭证），仅指明格式要求
			return nil, fmt.Errorf("条目格式非法（支持 JSON 对象或 token:appcode 逗号简写）")
		}
		m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("未解析出任何映射条目")
	}
	return m, nil
}

// Reload 解析新映射组并原子换表（SIGHUP 热更新入口）。
// 解析/校验失败时返回错误且不改动既有映射；换表后新增/吊销立即对后续请求生效。
// 返回换表后的映射组数量（失败时为当前数量，供日志留痕）。
func (r *MapResolver) Reload(spec string) (int, error) {
	m, err := buildAuthMap(spec)
	if err != nil {
		return r.Len(), fmt.Errorf("%s 热更新失败: %w", r.source, err)
	}
	r.m.Store(&m)
	return len(m), nil
}

// ReloadFromFile 从文件重载映射组（配合 EnvAuthMapFileKey + SIGHUP 使用）。
func (r *MapResolver) ReloadFromFile(path string) (int, error) {
	spec, err := readAuthMapFile(path)
	if err != nil {
		return r.Len(), fmt.Errorf("%s 热更新失败: %w", r.source, err)
	}
	return r.Reload(spec)
}

// Resolve 查表返回映射的 AppCode（未登记返回 false）。
func (r *MapResolver) Resolve(_ context.Context, token string) (string, bool) {
	m := r.m.Load()
	if m == nil {
		return "", false
	}
	appCode, ok := (*m)[token]
	return appCode, ok
}

// Len 返回映射组数量（启动日志/运维自检用，不含任何凭证内容）。
func (r *MapResolver) Len() int {
	m := r.m.Load()
	if m == nil {
		return 0
	}
	return len(*m)
}

// Source 返回加载来源描述（env 键名或文件路径），启动日志/排障用。
func (r *MapResolver) Source() string { return r.source }

// TokenFingerprint 返回 token 的 sha256 指纹（前 12 位 hex）。
// 供审计失败留痕使用：完整 Authorization 绝不落盘，仅以指纹区分不同 token 来源。
func TokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:6])
}
