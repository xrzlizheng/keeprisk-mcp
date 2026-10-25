# KeepRisk MCP 用户使用说明书

> 面向 **Trae / Cursor / Claude Code / Claude Desktop / Cline** 等 MCP 客户端的接入指南。
> 服务形态：MCP **Streamable HTTP**，按请求 Header `Authorization: <token>` 鉴权（兼容 `Bearer <token>` 前缀）。

---

## 目录

- [一、服务概览](#一服务概览)
- [二、前置条件](#二前置条件)
- [三、验证服务可用性](#三验证服务可用性)
- [四、客户端配置](#四客户端配置)
  - [4.1 Trae](#41-trae)
  - [4.2 Cursor](#42-cursor)
  - [4.3 Claude Code（CLI）](#43-claude-codecli)
  - [4.4 Claude Desktop（桌面版）](#44-claude-desktop桌面版)
  - [4.5 Cline（VSCode 插件）](#45-clinevscode-插件)
- [五、验证配置生效](#五验证配置生效)
- [六、首次使用示例](#六首次使用示例)
- [七、常见问题排查](#七常见问题排查)
- [八、运维管理（热更新）](#八运维管理热更新)
- [九、附录](#九附录)

---

## 一、服务概览

| 项目 | 说明 |
|------|------|
| Server 名称 | `keeprisk-enterprise`（v1.0.0） |
| 传输协议 | MCP Streamable HTTP（JSON-RPC over HTTP + SSE） |
| 工具数量 | **46 个**意图级工具 + 5 个 Agent-Native 入口 + 目录/直调/运维工具 |
| 数据覆盖 | 236 条 KeepRisk 网关接口（工商/风险/知产/经营/董监高/历史六大域） |
| 鉴权方式 | HTTP Header：`Authorization: <你的token>`（兼容 `Bearer <token>` 前缀） |
| 协议版本 | MCP `2024-11-05` |

**端点清单**（`/health`、`/ready` 除外，均需 Authorization）：

| 路径 | 方法 | 鉴权 | 说明 |
|------|------|------|------|
| `/health` | GET | 否 | 存活探针，返回 `{"status":"ok"}` |
| `/ready` | GET | 否 | 就绪探针，返回 catalog 加载状态与缓存/配额配置 |
| `/mcp` | POST | **是** | MCP 端点（JSON-RPC） |
| `/` | GET | 是 | 简要说明 |

> 来源：本项目 [README.md](file:///data/home2/zli/keeprisk-mcp/README.md)「快速开始」「架构概览」章节。

---

## 二、前置条件

### 1. 获取 token

KeepRisk MCP 通过 `Authorization: <token>` 鉴权，服务端将 token 映射为 AppCode 完成计费与配额隔离。请向 KeepRisk 平台管理员登记你的 token（**具体登记入口请咨询平台管理员**，本文不编造未验证的申请链接）。

- token 在每次 HTTP 请求的 Header 中携带，**不在服务端持久化**；服务端只保存 token→AppCode 映射。
- 服务端审计日志会对映射后的 AppCode 脱敏（如 `1361****`）；Authorization **不落盘**（401 时仅记 sha256 指纹用于滥用分析）。

### 2. 确认服务地址

服务已通过 Docker 部署。根据客户端与服务器的相对位置选择地址：

| 场景 | 地址 |
|------|------|
| 客户端与服务**同机**（本机部署机） | `http://localhost:8080/mcp` |
| 客户端在**同网段另一台机器** | `http://192.168.0.229:8080/mcp`（当前部署机 IP） |
| 跨网段 / 公网访问 | 需通过反向代理（nginx/caddy）提供 HTTPS 域名，**强烈建议**避免直接暴露 `:8080` |

> ⚠️ 当前 `docker-compose.yml` 将 8080 端口映射到 `0.0.0.0`，即局域网可直连。生产环境请加 TLS 与访问控制。

### 3. 客户端环境

- Trae / Cursor / Claude Code / Claude Desktop / Cline 任选其一
- Claude Desktop 桥接方案需 Node.js 18+（用于 `npx mcp-remote`）

---

## 三、验证服务可用性

配置客户端前，先用 `curl` 确认服务在线且鉴权机制正常。

### 探活（无需鉴权）

```bash
# 存活探针
curl -s http://localhost:8080/health
# 期望: {"status":"ok"}

# 就绪探针（确认 catalog 已加载、缓存/配额就绪）
curl -s http://localhost:8080/ready | python3 -m json.tool
# 期望: {"apis":236,"ready":true,...}
```

### 鉴权验证（无 Authorization 应返回 401）

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
# 期望: {"error":"unauthorized","message":"缺少 Authorization 请求头..."}
```

### MCP 握手验证（携带 Authorization）

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Authorization: 你的token" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl-test","version":"1.0"}}}'
# 期望: 返回 serverInfo {"name":"keeprisk-enterprise","version":"1.0.0"} + 六大域 instructions
```

> 上述三条命令已在本次部署验证通过（2026-08-02）。

---

## 四、客户端配置

> **通用要点**：keeprisk-mcp 使用 `Authorization` 请求头鉴权，**直接写 token 即可，无需 `Bearer ` 前缀**（`"Authorization": "<你的token>"`；也兼容标准 `"Bearer <token>"` 写法，二者服务端均接受）。

### 4.1 Trae

> 来源：Trae 官方文档 [docs.trae.ai/ide/add-mcp-servers](https://docs.trae.ai/ide/add-mcp-servers)（2026 验证）。

Trae 支持 stdio / SSE / Streamable HTTP 三种传输，HTTP 类型直接用 `url` + `headers`。

**方式一：UI 手动添加（推荐）**

1. 打开 Trae → 右上角**设置**图标 → 设置中心
2. 左侧导航选 **MCP**
3. 右上角 **添加 → 手动添加**
4. 粘贴以下 JSON，点击确认：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "<你的token>"
      }
    }
  }
}
```

**方式二：项目级配置（团队共享）**

在项目根目录创建 `.trae/mcp.json`，内容同上。需先在 **设置 → MCP** 中开启「启用项目级 MCP」开关。

> 配置文件全局位置（备选，直接编辑文件）：
> - macOS：`~/Library/Application Support/Trae/User/globalStorage/mcp.json`
> - Windows：`%APPDATA%\Trae\User\globalStorage\mcp.json`
>
> 来源：duoblog.com Trae MCP 集成实战（2026-03）。

**超时配置**（可选，工具调用较慢时添加）：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "<你的token>",
        "START_MCP_TIMEOUT_MS": "60000",
        "RUN_MCP_TIMEOUT_MS": "60000"
      }
    }
  }
}
```

**验证**：MCP 列表中该 Server 名称旁应显示**绿色**状态指示灯。红灯见 [七、常见问题排查](#七常见问题排查)。

---

### 4.2 Cursor

> 来源：本项目 [README.md](file:///data/home2/zli/keeprisk-mcp/README.md)「Cursor / MCP 客户端配置」章节（权威）。

Cursor 的 Streamable HTTP 配置与 Trae 同构。

**配置文件位置**：

- 全局：`~/.cursor/mcp.json`（所有项目可用）
- 项目级：`<项目根>/.cursor/mcp.json`（仅当前项目）

**配置内容**：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "<你的token>"
      }
    }
  }
}
```

**操作步骤**：

1. Cursor 顶栏 **Settings → Cursor Settings → Features → MCP**
2. 点 **+ Add New MCP Server**
3. Type 选 **HTTP**，Name 填 `keeprisk-enterprise`，URL 填 `http://localhost:8080/mcp`
4. 展开 **Headers**，添加 Key=`Authorization`，Value=`<你的token>`
5. 保存，列表中出现绿色圆点即成功

> 来源：README 配置示例；字段名随 Cursor 版本可能略有差异，以客户端 UI 为准。

---

### 4.3 Claude Code（CLI）

> 来源：thepromptshelf.dev「.mcp.json Configuration Reference 2026」、jangwook.net MCP サーバー構築ガイド（2026-04）。

Claude Code（CLI 客户端，非桌面版）原生支持 Streamable HTTP，配置字段为 `type: "http"` + `url`。

**配置文件位置**：

- 项目级（团队共享，提交到 VCS）：项目根目录 `.mcp.json`
- 用户级（所有项目可用）：`~/.claude.json`

**配置内容**：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "<你的token>"
      },
      "timeout": 60000
    }
  }
}
```

**首次加载**：项目级 `.mcp.json` 首次加载时 Claude Code 会弹安全确认（因文件可能来自他人仓库），确认信任即可。

**CLI 快捷添加**：

```bash
claude mcp add keeprisk-enterprise --transport http \
  --url http://localhost:8080/mcp \
  --header "Authorization: <你的token>"
```

---

### 4.4 Claude Desktop（桌面版）

> 来源：startdebugging.net「Fix: HTTP MCP Server URL Won't Connect in Claude Desktop」（2026-05，针对 Claude Desktop 0.13.x 验证）。

⚠️ **重要**：Claude Desktop 的 `claude_desktop_config.json` **只支持 stdio 类型**，直接写 `url` / `type:"http"` 会被校验拒绝（新版会静默丢弃整个 `mcpServers` 块）。本地 HTTP 服务需用 **`mcp-remote` 作为 stdio 桥接**。

**配置文件位置**：

| 系统 | 路径 |
|------|------|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

**配置内容（mcp-remote 桥接）**：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "command": "npx",
      "args": [
        "mcp-remote",
        "http://localhost:8080/mcp",
        "--header",
        "Authorization: ${KEEPRISK_TOKEN}"
      ],
      "env": {
        "KEEPRISK_TOKEN": "<你的token>"
      }
    }
  }
}
```

**说明**：
- `mcp-remote` 是社区维护的 stdio↔Streamable HTTP 桥接器，Claude Desktop 以 stdio 启动它，它再以 Streamable HTTP 连接本服务。
- 通过 `${KEEPRISK_TOKEN}` 变量引用避免 token 明文写在 args 里。
- 需本机预装 Node.js 18+（`npx` 随 Node 附带）。

**验证**：完全退出 Claude Desktop 后重启，在对话中应能看到 `keeprisk-enterprise` 的工具列表。若 Server 显示灰色/失败，展开看错误信息，多为 401（token 无效或未登记）或连接拒绝（服务未启动）。

> **远程公网服务备选方案**：若服务已通过 HTTPS 域名暴露且使用 OAuth/API Key，可走 `Settings → Connectors → Add custom connector` 直接填 URL，无需桥接。本服务当前为局域网 HTTP，不适用此方案。

---

### 4.5 Cline（VSCode 插件）

> 来源：scailabs.ai MCP 客户端通用说明（2026，适用于所有支持 streamable-http + 自定义 header 的客户端）。

Cline 支持 Streamable HTTP，配置与 Trae/Cursor 同构。

**配置文件**：`~/.cline/mcp_settings.json`（或通过 Cline 侧边栏 MCP 图标 UI 添加）

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "<你的token>"
      }
    }
  }
}
```

> 同模式适用于 Continue、Roo Code 等支持标准 streamable-http 传输的客户端，仅配置文件路径不同。

---

## 五、验证配置生效

### 方法一：MCP Inspector（推荐，独立于客户端验证）

> 来源：scailabs.ai MCP Inspector 验证章节。

MCP Inspector 是官方调试工具，跑通即代表客户端也能连。

```bash
npx @modelcontextprotocol/inspector \
  --url "http://localhost:8080/mcp" \
  --header "Authorization: <你的token>"
```

浏览器自动打开 Inspector UI → 点 **Connect** → 点 **List Tools**，应看到 46 个工具（`fuzzy_search`、`enterprise_credit_score` 等）。可在 UI 中直接试调一个工具确认往返正常。

### 方法二：客户端内验证

1. 在客户端对话中输入：
   > "列出 keeprisk 可用的工具"
   
   预期：Agent 调用 `tools/list`，返回工具清单。

2. 调用一个**不消耗网关**的工具验证链路：

   > "用 quota_status 查一下我当前的配额使用情况"
   
   预期：返回 `{"day_used":0,"day_limit":5000,"tokens":50,...}`（不含账户标识）。

3. 调用本地计费估算工具：

   > "用 estimate_billing 估算 batch_query_companies 传 2 个企业名要消耗几次网关"
   
   预期：返回 `{"estimated_calls":1,...}`（单次网关调用）。

---

## 六、首次使用示例

配置生效后，按以下场景与 Agent 对话即可（Agent 会自动编排工具调用）。

### 场景 A：供应商准入（最常用）

> "帮我查一下『字节跳动』的信用评分，再做一次标准尽调。"

Agent 工作流：
1. `resolve_entity` 锚定主体（避免同名串户）
2. `enterprise_credit_score` 四维评分
3. 按评分走 `enterprise_due_diligence_snapshot`（sections=basic,shareholders,admin_penalty,abnormal_operation,enforced,dishonest，digest_only=true）

### 场景 B：快速信用预审

> "用统一社会信用代码 91110108551385082Q 做一次 quick_scan。"

Agent 工作流：`fuzzy_search` → `company_quick_scan(depth=standard)`

### 场景 C：成本预估（0 次网关，先估再调）

> "estimate_billing 一下 enterprise_due_diligence_snapshot 关掉司法节（include_judicial=false）要几次 API。"

### 场景 D：董监高风险排查

> "张一鸣在字节跳动的任职风险画像 person_risk_profile。"

> 更多场景见 [eval/MCP_INSPECTOR_SCENARIOS.md](file:///data/home2/zli/keeprisk-mcp/eval/MCP_INSPECTOR_SCENARIOS.md)。

---

## 七、常见问题排查

### Q1：客户端显示 Server 红灯 / 连接失败

按顺序检查：

1. **服务是否在线**
   ```bash
   curl -s http://localhost:8080/health
   ```
   不通 → `docker compose ps` 看容器状态，`docker compose logs keeprisk-mcp` 看日志。

2. **token 是否正确且已登记**
   - 无 Authorization 头 → 401 `缺少 Authorization 请求头`
   - token 未登记/无效 → 401 `Authorization 无效或未登记`
   - token 已登记但映射的账户异常 → 工具调用时返回网关 103（账户不存在）/ 105（账号过期）

3. **地址是否可达**
   - 客户端与服务同机：用 `localhost`
   - 跨机：用 `192.168.0.229`，确认防火墙放行 8080

4. **Header 名与格式是否正确**
   - 必须是 `Authorization: <token>`（直接写 token，**无需 `Bearer ` 前缀**；写了 `Bearer ` 前缀也接受，scheme 大小写不敏感）
   - 旧的 `AppCode` / `X-AppCode` 头**不再作为凭证**：仅携带它们而无 Authorization → 401

### Q2：Claude Desktop 配置后工具列表为空

`claude_desktop_config.json` 不支持直接写 `url`。改用 [4.4 mcp-remote 桥接方案](#44-claude-desktop桌面版)。

### Q3：工具调用返回 109（资金不足）

token 映射的账户余额不足，联系 KeepRisk 平台充值。

### Q4：工具调用返回 110

**110 = 成功但无记录**，不是错误。Agent 会按 instructions 将其表述为「无记录」而非失败。

### Q5：超时 / 工具调用卡住

尽调类工具（`enterprise_due_diligence_snapshot`）默认开启 11 个司法节，单次较慢。建议：
- 先用 `digest_only=true` 扫描
- 用 `include_judicial=false` 关闭司法节
- 用 `sections=` 显式指定需要的节

客户端侧可在 headers 加 `RUN_MCP_TIMEOUT_MS: 60000`（Trae）或配置 `timeout` 字段（Claude Code）。

### Q6：跨机访问被拒

当前 `docker-compose.yml` 绑定 `0.0.0.0:8080`，理论上局域网可达。若不通：
- 检查部署机防火墙：`sudo ufw status` / `sudo iptables -L`
- 确认客户端机器与 `192.168.0.229` 同网段

---

## 八、运维管理（热更新）

> 面向**开发者 / 运维人员**的服务运维指南。
> 版本：v1.1 · 2026-08-03

### 8.1 热更新概述

KeepRisk MCP 服务支持**零停机热更新**，无需重建镜像或重启容器即可更新代码。

**传统方式 vs 热更新对比**：

| 方式 | 步骤 | 停机时间 | 复杂度 |
|------|------|----------|--------|
| 传统方式 | `git pull` → `docker compose down` → `docker compose up -d` | ~15s | 高 |
| **热更新** | `bash hot-reload.sh`（一键完成） | <1s | 低 |

### 8.2 热更新原理

```
┌─────────────────────────────────────────────────────────────┐
│                        工作流程                               │
├─────────────────────────────────────────────────────────────┤
│  1. 编译新二进制文件到 ./build/keeprisk-mcp                   │
│                          ↓                                    │
│  2. Docker Volume 挂载使容器内 /keeprisk-mcp 变为新版本        │
│                          ↓                                    │
│  3. 发送 SIGHUP 信号给容器进程                                │
│                          ↓                                    │
│  4. 应用检测到挂载路径有可执行文件                            │
│                          ↓                                    │
│  5. 使用 syscall.Exec 替换当前进程（真正的热替换）             │
│                          ↓                                    │
│  6. 新进程启动，加载新代码                                    │
└─────────────────────────────────────────────────────────────┘
```

### 8.3 使用方法

#### 方式一：使用热更新脚本（推荐）

```bash
# 完整流程：拉取代码 + 编译 + 热更新
bash hot-reload.sh

# 仅编译 + 热更新（跳过 git pull）
bash hot-reload.sh --skip-pull

# 查看帮助
bash hot-reload.sh --help
```

**执行日志示例**：

```
==========================================
  KeepRisk MCP 热更新
==========================================

[1/4] 拉取最新代码...
  已是最新代码

[2/4] 编译二进制文件...
  编译成功: ./build/keeprisk-mcp
  -rwxrwxr-x 1 zli zli 8.1M  8月  3 10:49 ./build/keeprisk-mcp

[3/4] 发送热更新信号...
  正在查找容器内主进程...
  SIGHUP 信号已发送

[4/4] 验证服务状态...
  等待服务启动...
  ✅ 服务已就绪！
```

#### 方式二：手动分步执行

```bash
# 步骤 1：编译新二进制
CGO_ENABLED=0 GOOS=linux /usr/local/go/bin/go build \
  -trimpath -ldflags="-s -w" \
  -o build/keeprisk-mcp ./cmd/keeprisk-mcp

# 步骤 2：发送 SIGHUP 信号
docker kill --signal=SIGHUP keeprisk-mcp-keeprisk-mcp-1

# 步骤 3：验证服务状态
curl -s http://localhost:8080/ready | python3 -m json.tool
# 期望: {"ready":true, ...}
```

### 8.4 配置说明

#### docker-compose.yml 关键配置

```yaml
services:
  keeprisk-mcp:
    volumes:
      # 二进制文件挂载：实现热更新
      # 将本地编译的二进制挂载到容器内，容器启动时使用该文件
      - ./build/keeprisk-mcp:/keeprisk-mcp:ro
```

#### main.go 热更新处理

```go
// 监听 SIGHUP 信号
func handleHotReload() error {
    sighupCh := make(chan os.Signal, 1)
    signal.Notify(sighupCh, syscall.SIGHUP)

    go func() {
        for range sighupCh {
            // 检查挂载的二进制文件是否存在
            mountedPath := "/keeprisk-mcp"
            if info, err := os.Stat(mountedPath); err == nil && info.Mode()&0111 != 0 {
                binPath = mountedPath
            }
            // 使用 syscall.Exec 替换当前进程
            syscall.Exec(binPath, os.Args, os.Environ())
        }
    }()
    return nil
}
```

### 8.5 日志验证

热更新成功后，容器日志会显示如下信息：

```
2026/08/03 02:49:13 received SIGHUP signal, reloading...
2026/08/03 02:49:13 hot reload: restarting with /keeprisk-mcp
2026/08/03 02:49:13 keeprisk-mcp listen :8080 mcp=/mcp (Authorization required except /health, /ready)
```

**查看日志命令**：

```bash
# 查看最近 50 行日志
docker logs keeprisk-mcp-keeprisk-mcp-1 --tail 50

# 实时查看日志
docker logs -f keeprisk-mcp-keeprisk-mcp-1
```

### 8.6 注意事项

| 项目 | 说明 |
|------|------|
| **编译要求** | 必须使用 `CGO_ENABLED=0 GOOS=linux` 确保 Linux 静态二进制 |
| **容器状态** | 热更新前确保容器在运行中，否则请使用 `docker compose up -d` 启动 |
| **信号处理** | 发送信号后服务会在毫秒级内重启，SSE 连接可能短暂断开 |
| **环境变量变更** | 热更新（SIGHUP）继承**启动时**的环境变量：修改 `KEEPRISK_AUTH_MAP` 等配置后需 `docker compose up -d` 重建容器，仅发信号不生效 |
| **版本回退** | 如需回退，只需编译旧版本代码并重新发送 SIGHUP |
| **开发环境** | 开发时建议使用此方式快速迭代，避免频繁重建容器 |

### 8.7 故障排查

| 问题 | 原因 | 解决方案 |
|------|------|----------|
| `docker exec` 执行 `kill` 失败 | distroless 镜像无 shell | 使用 `docker kill --signal=SIGHUP` |
| 热更新后服务未响应 | 新二进制编译失败 | 检查编译日志，重新执行 `go build` |
| 服务无法启动 | 二进制文件损坏或权限问题 | 检查文件大小和可执行权限：`ls -la build/keeprisk-mcp` |
| `syscall.Exec` 失败 | 挂载路径无权限 | 确认 `./build/keeprisk-mcp` 有执行权限 |

### 8.8 传统部署方式（备选）

如果热更新不适用，仍可使用传统方式：

```bash
# 完整重建流程
git pull
docker compose down
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o build/keeprisk-mcp ./cmd/keeprisk-mcp
docker compose up -d --build
```

---

## 九、附录

### A. 完整端点与鉴权对照

| 端点 | 鉴权 | 用途 |
|------|------|------|
| `GET /health` | 否 | 存活探针 |
| `GET /ready` | 否 | 就绪探针（catalog/缓存/配额） |
| `POST /mcp` | **是** | MCP JSON-RPC 端点 |
| `GET /` | 是 | 说明文本 |

### B. 网关状态码

| code | 含义 | 处理建议 |
|------|------|----------|
| 100 | 查询成功 | 正常 |
| 110 | 成功，无记录 | 非错误，表述为「无记录」 |
| 101 | 参数错误 | 检查工具入参 |
| 103 | 账户不存在 | token 映射的账户不存在（未登记或已注销） |
| 104 | IP 限制 | 联系平台加白 |
| 105 | 账号过期 | token 映射的账户过期 |
| 107 | 服务不存在 | pcode 错误 |
| 109 | 资金不足 | 充值 |

> 来源：[README.md 网关状态码](file:///data/home2/zli/keeprisk-mcp/README.md)。

### C. 推荐工具优先级

| 优先级 | 工具 | 场景 |
|--------|------|------|
| P0 | `resolve_entity` | 模糊名→候选列表，避免同名串户 |
| P0 | `fuzzy_search` | 统一模糊入口（企业名/信用代码/电话/人名） |
| P0 | `enterprise_credit_score` | 信用评分门卫（≥80 快速通过 / 50-79 标准尽调 / <50 深度排查） |
| P0 | `estimate_billing` | 调用前本地计费估算（0 次网关） |
| P1 | `company_quick_scan` | 一句话查企业（工商+信用分+风险摘要） |
| P1 | `enterprise_due_diligence_snapshot` | 26 维度尽调 |
| 运维 | `quota_status` | 查当前账户配额（不消耗网关） |

### D. 信息来源声明

本文档关键配置事实的来源（符合项目 AGENTS.md 溯源规则）：

| 章节 | 来源 | 类型 |
|------|------|------|
| 服务端点 / 鉴权 / 工具清单 | 本项目 README.md | 已验证（部署测试通过） |
| Trae 配置 | [docs.trae.ai/ide/add-mcp-servers](https://docs.trae.ai/ide/add-mcp-servers) | 官方文档 |
| Cursor 配置 | 本项目 README.md | 已验证 |
| Claude Code 配置 | thepromptshelf.dev（2026-06）、jangwook.net（2026-04） | 社区验证 |
| Claude Desktop stdio 限制 / mcp-remote 桥接 | startdebugging.net（2026-05，Claude Desktop 0.13.x 验证） | 社区验证 |
| MCP Inspector 验证法 | scailabs.ai MCP 教程 | 社区验证 |
| token 登记入口 / 映射配置 | 未验证 | 咨询平台管理员（服务端经 KEEPRISK_AUTH_MAP 配置映射） |
| 热更新实现 | 本项目 main.go / docker-compose.yml | 已验证（SIGHUP 测试通过） |

---

**文档版本**：v1.2 · 2026-09-04 · 鉴权切换为 `Authorization: Bearer <token>`

- v1.0 · 2026-08-02 · 初始版本，基于本机 Docker 部署（容器 `keeprisk-mcp-keeprisk-mcp-1`，`192.168.0.229:8080`）编写
- v1.1 · 2026-08-03 · 添加热更新功能文档、hot-reload.sh 使用说明、故障排查指南
- v1.2 · 2026-09-04 · 入站鉴权由自定义 `AppCode` Header 切换为 `Authorization: <token>`（token→AppCode 映射由服务端 env 配置，兼容 `Bearer ` 前缀）；`AppCode`/`X-AppCode` 头不再作为凭证；工具输出全面脱敏（编码/网关地址/配置名不外泄）
