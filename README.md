# KeepRisk Enterprise MCP（Go 生产版）

面向 **企查查 / 天眼查类企业数据网关** 的 [Model Context Protocol (MCP)](https://modelcontextprotocol.io) 服务：将 **236** 条 KeepRisk 数据接口封装为 **46+ 意图级工具** + **5 个 Agent-Native 入口** + 目录发现 + 直调 pcode。

本仓库为 **远程部署推荐形态**：**Streamable HTTP**、**按请求 Header 鉴权（Authorization）**、配额/缓存/审计/健康检查，适合多租户与容器化远程部署。

设计理念对标 [企查查智能体数据平台](https://agent.qcc.com/data)（Agent-Native）：六大域分层、反幻觉元数据、开箱即用的一键扫描工具。

> **需要 API Key？** 本仓库不含任何凭证。入站 token 与网关 AppCode 由管理员统一发放，联系开通：**[https://github.com/xrzlizheng](https://github.com/xrzlizheng)**。详见 [网关开通与联系](#网关开通与联系)。

---

## 目录

- [Agent-Native 六大域（对标企查查）](#agent-native-六大域对标企查查)
- [架构概览](#架构概览)
- [网关开通与联系](#网关开通与联系)
- [与 Python 版对比](#与-python-版对比)
- [快速开始](#快速开始)
- [环境变量](#环境变量)
- [MCP 工具一览](#mcp-工具一览)
- [Agent 工作流指南](#agent-工作流指南)
- [资源](#资源)
- [鉴权与多租户](#鉴权与多租户)
- [计费与尽调快照](#计费与尽调快照)
- [运维与可观测性](#运维与可观测性)
- [Docker](#docker)
- [Cursor / MCP 客户端配置](#cursor--mcp-客户端配置)
- [目录与接口元数据](#目录与接口元数据)
- [开发构建](#开发构建)
- [网关状态码](#网关状态码)
- [安全说明](#安全说明)
- [相关文档](#相关文档)
- [打赏支持](#打赏支持)
- [License](#license)

---

## Agent-Native 六大域（对标企查查）

参考 [企查查智能体数据平台](https://agent.qcc.com/data) 的 6 大 MCP Server 分层，KeepRisk 在**单 Server** 内用 `domain_playbook`（`list_data_capabilities` 返回）组织同等能力域：

| 域 ID | 名称 | 企查查对照 | 核心工具 |
|-------|------|------------|----------|
| `keeprisk-company` | 企业基座 | qcc-company | `fuzzy_search`, `resolve_entity`, `company_quick_scan`, `control_penetration` |
| `keeprisk-risk` | 风控大脑 | qcc-risk | `enterprise_credit_score`, `enterprise_due_diligence_snapshot`, `compare_enterprises` |
| `keeprisk-ipr` | 知产引擎 | qcc-ipr | `intellectual_property_portfolio`, `data_badge_full` |
| `keeprisk-operation` | 经营罗盘 | qcc-operation | `operational_compliance`, `supply_chain_relations`, `search_enterprises` |
| `keeprisk-executive` | 董监高画像 | qcc-executive | `person_risk_profile`, `enterprise_deep_profile` |
| `keeprisk-history` | 历史轨迹 | qcc-history | `entity_risk_timeline`, `annual_report_detail` |

### Agent-Native 新增工具（开箱即用）

| 工具 | 网关次数 | 说明 |
|------|----------|------|
| `fuzzy_search` | 1–2 | 统一模糊入口：企业名/信用代码/电话/人名自动路由 |
| `company_quick_scan` | ~12 | 一句话查企业：工商 + 信用分 + 风险 digest |
| `compare_enterprises` | ~10/家 | 2–5 家企业横向信用与风险对比 |
| `person_risk_profile` | 3–4 | 人员被执行/失信/任职（查老板） |
| `entity_risk_timeline` | 4 | 变更/处罚/立案/经营异常时间线 |

所有聚合工具响应含 `_agent_meta`：`data_source`、`queried_at`、`empty_means`（code=110 解释），降低大模型幻觉。

与企查查差异：KeepRisk 为单 Server + Authorization 鉴权；企查查为 6 独立 Stream URL + Bearer Token + 积分体系。详见 [接入指南](https://agent.qcc.com/guide)。

---

## 架构概览

```
┌─────────────────┐  Authorization token    ┌──────────────────────┐
│  MCP Client     │ ───────────────────────►│  keeprisk-mcp (Go)   │
│  (Cursor/Agent) │     POST /mcp (JSON-RPC)│  Streamable HTTP     │
└─────────────────┘                         └──────────┬───────────┘
                                                       │
                       ┌───────────────────────────────┼───────────────────────────────┐
                       │                               │                               │
                       ▼                               ▼                               ▼
              ┌────────────────┐              ┌────────────────┐              ┌────────────────┐
              │ catalog (embed) │              │ quota / cache  │              │ audit (JSONL)  │
              │ 236 APIs        │              │ per AppCode    │              │ 脱敏主体 hash  │
              └────────────────┘              └────────────────┘              └────────────────┘
                                                       │
                                                       ▼
                                            ┌──────────────────────┐
                                            │ KeepRisk API Gateway │
                                            │ POST /api/data       │
                                            │ Header: AppCode      │
                                            │ （服务端映射后注入）  │
                                            └──────────────────────┘
```

| 层次 | 职责 |
|------|------|
| **传输** | MCP Streamable HTTP（`MCP_HTTP_PATH`，默认 `/mcp`） |
| **鉴权** | HTTP `Authorization: <token>`（兼容 `Bearer <token>` 前缀；token 经服务端映射为 AppCode）；缺失/未登记 → **401**（`/health`、`/ready` 除外） |
| **工具路由** | 47 个意图级工具（主体锚定、信用评分、尽调、聚合画像、产业链等）+ `keeprisk_query` 兜底 |
| **目录** | 内嵌 `full_catalog.json`，多版本 pcode 自动解析到最新 |
| **防护** | 按 AppCode 日配额、令牌桶速率、并发上限；可选 LRU 响应缓存 |
| **合规留痕** | 异步审计：工具名、pcode、耗时、AppCode 脱敏、查询主体 **仅 hash**；HTTP 层返回 `X-Request-ID` |

---

## 网关开通与联系

本服务是 **[KeepRisk 企业数据网关](https://api.keeprisk.com:9080)**（企查查 / 天眼查类企业数据：工商、司法、知产、招投标、产业链）的 MCP 接入层——**236 条数据接口、47 个意图级工具、5 个 Agent-Native 入口**，让任意 MCP 客户端（Cursor、Claude、各 Agent 平台）直接用自然语言完成企业尽调与风险排查。

**API Key 如何获取：**

- 本仓库**开源代码，不含任何凭证**（token / AppCode 一律不入库）；
- 入站授权 token 与上游网关 AppCode 由管理员统一生成发放；
- 联系开通（任选其一）：
  - **微信**：扫码添加好友（备注「网关开通」）—— ![微信二维码](docs/wechat_contact_qr.png)
  - **GitHub**：**[https://github.com/xrzlizheng](https://github.com/xrzlizheng)** —— 提 Issue 或 Discussion 均可，注明使用场景与预估调用量。

开通后你会得到一组 `token → AppCode` 映射，填入 `.env`（见 [快速开始](#快速开始)）即可使用。计费以网关侧为准，调用前可用内置 `estimate_billing` 工具预估成本。

---

## 与 Python 版对比

| 维度 | **keeprisk-mcp-go**（本仓库） | **keeprisk-mcp**（Python） |
|------|-------------------------------|----------------------------|
| 传输 | Streamable HTTP | stdio |
| AppCode | **每次 HTTP 请求 `Authorization: <token>`**（服务端映射为 AppCode，多租户） | 环境变量 `KEEPRISK_APPCODE` |
| 工具数量 | **47 个**（含 20+ 聚合画像工具） | 较少（无聚合层） |
| 主体锚定 | `resolve_entity` | 无 |
| 信用评分门卫 | `enterprise_credit_score`（6 次 API） | 无 |
| 尽调聚合 | `enterprise_due_diligence_snapshot`（26 维度） | 无 |
| 批量工商 | `batch_query_companies`（≤100） | 无 |
| 股权/实控 | `control_penetration` / `actual_controller` | 无 |
| 计费预估 | `estimate_billing`（0 次网关） | 无 |
| 配额/缓存/审计 | 有 | 无 |
| 读 Markdown 接口文档 | 无（用 `get_keeprisk_api` + 目录） | `read_keeprisk_api_doc` |
| 适用场景 | **生产、共享服务、容器化部署** | **本机 Cursor、目录构建脚本** |

两版 **Server 名称均为 `keeprisk-enterprise`**，底层 pcode 一致；生产环境请以 Go 版为准。

---

## 快速开始

### 前置条件

- Go **1.23+**（构建）
- 网关根地址 **`KEEPRISK_API_BASE`**（无尾斜杠，如 `https://api.keeprisk.com:9080`）
- 至少一组 **token→AppCode 映射**（`KEEPRISK_AUTH_MAP` env 或 `KEEPRISK_AUTH_MAP_FILE` 文件，token 由管理员发放——[联系开通](#网关开通与联系)）

### 本地运行

```bash
# 1. 复制配置模板并填入管理员发放的凭证（.env 已被 gitignore，不会入库）
cp .env.example .env
$EDITOR .env

# 2. 启动
go run ./cmd/keeprisk-mcp
```

服务默认监听 `:8080`：

| 路径 | 说明 |
|------|------|
| `GET /health` | 存活探针 |
| `GET /ready` | 就绪（catalog 已加载、客户端已初始化） |
| `POST /mcp` | MCP 端点（需 `Authorization`） |
| `GET /` | 简要说明文本 |

### 探活示例

```bash
curl -s http://localhost:8080/ready | jq .
curl -s -H "Authorization: YOUR_TOKEN" http://localhost:8080/mcp
```

---

## 环境变量

### 必填（调用网关）

| 变量 | 说明 |
|------|------|
| `KEEPRISK_API_BASE` | 网关根 URL，必须 `http://` 或 `https://`；不可为占位 `host:port` |
| `KEEPRISK_AUTH_MAP` | 入站鉴权映射组（env 引导来源）：`Authorization` token → AppCode。支持 JSON 对象（推荐，如 `{"kpm_xxx":"AppCodeA"}`）或 `token:appcode` 逗号简写；解析失败**启动即报错**（fail-fast）。token 字符格式无限制（不含空格即可）。生产经 secrets/配置中心注入，勿写入镜像层。env 无法从进程外修改，此模式变更需**重启进程** |
| `KEEPRISK_AUTH_MAP_FILE` | 映射组文件（热更新来源，推荐）：配置后文件为**唯一来源**（env 被忽略；缺失/非法启动即报错，不静默退回 env）。内容与 env 同格式。宿主改文件后 `kill -HUP` / `docker kill --signal=HUP <容器>` 即生效——进程内原子换表、**会话不断**；换表先校验，非法变更保留旧映射。SIGHUP 同时兼容二进制热替换：磁盘二进制 mtime 有变才 exec，否则仅做授权热更 |

### HTTP / MCP

| 变量 | 默认 | 说明 |
|------|------|------|
| `MCP_HTTP_ADDR` | `:8080` | 监听地址 |
| `MCP_HTTP_PATH` | `/mcp` | MCP 路径 |

### 网关客户端

| 变量 | 默认 | 说明 |
|------|------|------|
| `KEEPRISK_REQUIRE_TLS` | - | `true` 时禁止 `http://` 的 base |
| `KEEPRISK_MAX_INFLIGHT` | `200` | 全局网关并发上限 |
| `KEEPRISK_HTTP_TIMEOUT` | `60s` | 单次 HTTP 超时 |
| `KEEPRISK_MAX_RESPONSE_MB` | `4` | 网关响应体大小上限（MB） |

### 缓存

| 变量 | 默认 | 说明 |
|------|------|------|
| `KEEPRISK_CACHE_TTL_SEC` | `0`（关闭） | TTL（秒）；启用建议 60–300 |
| `KEEPRISK_CACHE_MAX` | `1000` | 最大条目数 |
| `KEEPRISK_CACHE_PER_APPCODE` | `true` | 缓存按 AppCode 隔离（安全默认，防伪造 AppCode 跨租户读旁路）；显式 `false` 关闭隔离（成本优先，不推荐） |

命中缓存的响应带 `_cache_hit: true`，**不计入配额、不调网关**。

### 配额与限流（按 AppCode，内存态，重启清零）

| 变量 | 默认 | 说明 |
|------|------|------|
| `KEEPRISK_QUOTA_DAILY` | `5000` | 每日调用上限；`0` 不限 |
| `KEEPRISK_RATE_PER_SEC` | `50` | 每秒令牌桶 |
| `KEEPRISK_RATE_BURST` | 同 rate | 突发容量 |
| `KEEPRISK_CONCURRENT_PER_APP` | `10` | 单 AppCode 并发 |

工具 **`quota_status`** 可查询当前 AppCode 状态（不消耗网关）。

### 审计

| 变量 | 说明 |
|------|------|
| `KEEPRISK_AUDIT_PATH` | 空或 `-` → stderr；否则写入文件路径（JSON Lines） |
| `KEEPRISK_AUDIT_ASYNC` | `false` 可同步写（排障） |

### 输出格式

| 变量 | 说明 |
|------|------|
| `KEEPRISK_PRETTY_JSON` | `true` 时工具返回缩进 JSON（默认紧凑，省 Token） |
| `KEEPRISK_HIDE_PCODE` | `true`（默认）时**所有对客输出**统一脱敏：移除 `pcode_used`/`pcode`/`base_url`/`apikey`/`app_code` 等敏感键（递归、大小写不敏感），文本中的接口编码替换为「〔内部接口〕」、`KEEPRISK_*` 配置名替换为「〔内部配置〕」、错误/提示文案中的 URL 替换为「〔网关地址〕」（业务数据中的 URL 不受影响）；目录类工具（`keeprisk_query`/`get_keeprisk_api`/`search_keeprisk_apis`/`validate_keeprisk_params`）**不再豁免**，已持码调用方仍可经入参直调。`false` 关闭（**仅调试用，启动日志显著告警，切勿在生产开启**）。审计日志不受影响 |

---

## MCP 工具一览

共 **47 个主工具** + **5 个 Agent-Native 工具** + 目录/直调/运维工具。

### Agent-Native 入口（对标企查查，建议优先）

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `fuzzy_search` | 1–2 | 统一模糊搜索（企业/信用代码/电话/人名） |
| `company_quick_scan` | ~12 | 一句话查企业（工商+信用分+风险摘要） |
| `compare_enterprises` | ~10/家 | 2–5 家企业横向对比 |
| `person_risk_profile` | 3–4 | 董监高/人员风险 |
| `entity_risk_timeline` | 4 | 变更与风险事件时间线 |

### P0 — 主体锚定与信用门卫（建议优先）

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `resolve_entity` | 1 | 模糊企业名 → 候选列表（含 `company_id`/`credit_code`/法人/地区/状态），**避免同名串户** |
| `enterprise_credit_score` | 6 | 四维评分 + 角标 + 标签 + 风险等级；≥80 快速通过 / 50–79 标准尽调 / <50 深度排查 |
| `estimate_billing` | 0 | 调用前本地计费估算（按工具名与 `sections` 等参数） |

### 基础查询与尽调

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `enterprise_basic_profile` | 1 | 单家企业工商照面 |
| `enterprise_due_diligence_snapshot` | 按节 | **26 维度**并行尽调/风险扫描（见下节） |
| `batch_query_companies` | 1 | 批量工商核验（单次 API，≤100 条） |
| `actual_controller` | 1 | 实际控制权/控制链 |
| `control_penetration` | 1 | 股权穿透：对外投资 + 直接股东 |
| `search_enterprises` | 1 | 多条件企业筛选 V4 |

### 企业聚合画像（Phase 4–6）

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `enterprise_extended_profile` | 9 | 工商 + 股东 + 人员 + 标签 + UBO + 实控 + 穿透 + 股权出质 + 角标 |
| `enterprise_deep_profile` | 7 | 董监高 + 注册地核验 + 坐标 + 曾用名 + 联系方式 + 关联信息 |
| `enterprise_business_detail` | 8 | 分支机构 + 变更 + 标讯 + 信用中国许可/处罚 + 实际股东 + 注销等 |
| `enterprise_graph_relations` | 3 | 关联信息 + 股权穿透节点 + 实质关联图谱 |
| `enterprise_development` | 5 | 核心团队 + 融资历史 + 竞品 + 业务 + 历程 |
| `enterprise_evaluation_report` | 2 或 5 | 默认仅查创始团队 + 技术竞争力；提供真实 `province` 后才追加行业饱和度/垄断/成长 |
| `data_badge_full` | 4 | 司法 + 企业 + 经营 + 知识产权四维角标 |
| `operational_compliance` | 4 | 行政许可 + 抽查检查 + 资质证书 + 进出口信用 |
| `judicial_deep_dive` | 6 | 破产详情 + 司法冻结/到期/续冻/解冻 + 股东变更 |
| `intellectual_property_portfolio` | 5 | 专利 + 软著 + 作品著作权 + 域名 + 商标 |
| `annual_report_investments` | 4 | 年报 + 年度财务 + 财务指标 + 对外投资 V2 |
| `annual_report_detail` | 8 | 级联查最新年报 7 个子表（可传 `report_id` 省 1 次） |

### 上市公司与招商

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `listed_company_profile` | 8 | 上市概况 + 指标 + 行情 + 股东 + 股本 + 主营 + 评级 |
| `listed_company_financials` | 13 | 资产负债表 + 年度/单季报表 + 财务比率 + 分红 + 募资等 |
| `listed_company_market` | 17 | 股权质押 + 限售解禁 + 回购 + 增减持 + 机构持仓 + 研报评级等 |
| `listed_company_extended` | 10 | 银保监处罚 + 高管 + 行业分析 + 龙虎榜 + A 股发行 |
| `supply_chain_relations` | 3 | 供应商/客户 + 产业链图谱 + 节点统计 |
| `investment_readiness_score` | 4 | 投资意愿 + 综合评分 + 角标（招商场景） |
| `investment_analysis_suite` | 7 | 招聘 + 专利 + 财务健康 + 投资行为 + 股东 + 研发 |
| `investment_event_profile` | 5 | 投资事件 + 趋势 + 合作机构排名 + 投资机构详情 |

### 产业链与园区

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `search_chains` | 1 | 产业链模糊搜索 |
| `get_chain_nodes` | 1 | 产业链节点树 |
| `search_chain_enterprises` | 1 | 产业链企业筛选 |
| `industry_chain_profile` | 4 | 产业链数据分析 + 地区/节点/财务统计 |
| `school_alumni_circle` | 院校 max_pages 页（简称 +1；stats +1）／企业 2+max_pages（+stats +1） | 校友圈企业（校企）：双入口——`university_name`（院校全称，简称如 中科大 自动解析）或 `company_name`（企业名，由其法人院校反查：工商基本信息→companyId 维度人员学历→按院校筛校友圈，含 `via_company` 推导链）；含校友学历二次校验；支持 `province`/`city`/`county` 区划（内部构造上游 regionList，缺省 province 本地报错）；支持 `page`/`max_pages` 真实分页（上游 pageNo），响应含 `pages_scanned`/`upstream_total`/`_sampling`；`include_stats=true` 附地域/行业分布 |
| `industrial_park_search` | 0–4 | 支持 `park_name -> parkId -> 入驻企业` 级联查询，也支持直接传 `park_id` 直查；地区筛选额外 1 次 |

### 目录、直调与运维（本地 / 兜底）

| 工具 | 网关次数 | 场景 |
|------|----------|------|
| `keeprisk_query` | 1 | 直调 236+ 接口；`pcode` 可传**接口编码或接口名称**（如 `院校信息`）。名称解析规则：精确匹配 > 唯一包含匹配；歧义时列出候选拒调，未知时报错——均不触达网关、不计费 |
| `search_keeprisk_apis` | 0 | `mode=search` 关键词 / `mode=resolve` 意图匹配 |
| `list_categories` | 0 | 产品线 / 分类浏览 |
| `list_data_capabilities` | 0 | 能力清单与 Agent 剧本（含 30+ 场景） |
| `get_keeprisk_api` / `validate_keeprisk_params` | 0 | 元数据 / 本地校验（同样支持编码或名称） |
| `quota_status` | 0 | 配额与限流状态 |

**名称直调**：`KEEPRISK_HIDE_PCODE` 开启时接口编码不出现在对客输出，`search_keeprisk_apis` 检索到的接口名称是 Agent 调用未封装领域接口的唯一合法句柄——`search_keeprisk_apis` 拿名称 → `get_keeprisk_api` 确认参数 → `keeprisk_query` 按名称直调，闭环无需持码。

**参数要求机制（Param Rules）**：把实测确认的参数传法要求集中定义（`internal/catalog/paramrules.go`），
在模型会经过的四条路径自动浮现，解决"模型不知道该怎样传参"的根本问题：

| 路径 | 呈现 |
|---|---|
| `search_keeprisk_apis` / `get_keeprisk_api` | 结果含 `param_requirements`（含 `note` 原因与 `fix` 正确写法）、`doc_params`（文档枚举值）、`example_source`（示例来源） |
| `validate_keeprisk_params` | 违规进 `errors`（阻断型）/ `warnings`（提示型） |
| `keeprisk_query` | 阻断型**调用前拦截**（零网关调用、不计费）；提示型随**成功响应**返回 `warnings` |

规则分三级：`required`（实测必填）/ `format`（容器格式要求）/ `ignored`（实测被静默忽略）。
阻断型仅用于"参数被静默忽略且本接口内存在正确写法"——阻止调用即避免错误数据与无效计费。
每条规则都要求有线上实测证据（注释中注明），接口格式统一后应删除对应规则。

**参数知识以接口文档 PDF 原件为权威来源**（`/data/home2/zli/接口文档pdf/`，241 份 `[PCODE]标题.pdf`）：

```bash
python3 eval/extract_param_pdf.py --stats      # 从 PDF 表格结构提取参数（覆盖率 91%）
python3 eval/merge_param_meta.py               # 生成 internal/catalog/data/param_meta.json
```

> **为何用 PDF 而非 markdown**：OCR markdown 把参数表拍扁成一行（"参数名称 参数说明参数类型说明"
> 全挤在一起），提取覆盖率仅 6%；PDF 有真实文本层与表格结构，提取 **1010 个参数（91%）**，
> 并首次暴露 **106 个接口存在目录未列出的参数**（如 `工商基本信息` 的 `companyId`、
> `院校信息` 的 `almaMater`）。

构建期合并（`internal/catalog/parammeta.go`）：
**文档示例优先**（修复目录示例残缺——目录示例常缺主体参数，模型照抄必失败）、
**补充目录未列出的参数**（含完整枚举值，如 `labelList` 的 14 种科技资质、`type` 的 4 种取值）。

> ⚠️ **教训（七条）**：
> 1. 参数结论必须**先读原始文档**。本仓库曾三次因"用错误参数名的失败否定整个能力"而误判
>    （`pageNo` 分页、企业反查院校、`searchKey` 对象格式），每次都由试探式实测导致。
> 2. **换样本验证**是防止"巧合匹配即下结论"的必要步骤。`院校信息` 的 `almaMater` 用目标企业
>    查询时首位恰好正确，但换三家企业首位恒为同一院校——当时因此误判"参数无效"。
> 3. **"实测不生效"与"用法不对"是两回事**。`almaMater` 实为**置顶参数**（传院校名则精确匹配置顶，
>    不传则 `sortFlag=false` 时账户默认母校置顶）——上一条的"恒为同一院校"根因是**账户默认母校**，
>    而非参数无用。**看响应变化只能证伪用法，不能证明参数无用**。
> 4. **"参数无效"不等于"接口无用"**：无效参数常触发**兜底返回**（默认列表/默认排序），
>    看起来像正常结果。`院校信息` 传无效的 `entName` 时返回按排名的默认院校列表，
>    我曾据此把接口用途误述为"查榜单"——实际它是**院校信息查询**
>    （`universityName` → 标签/简介/类型/图片地址；`province` → 该省企业+高校标签）。
>    **判定接口用途的唯一依据是文档的设计说明，而非无效参数下的响应内容**。
> 5. **修复动作本身需要验证，且要防"过度修正"**：本仓库曾有 4 个 bug 是修 bug 时引入的——
>    提取器从"混入响应字段"修到"整批丢参"、从"放行无效参数"修到"拦掉文档正确用法"。
>    测试也要验证其**校验力**（变异测试：注入错误确认测试会失败。
>    `determinism_test.go` 第一版用 `Default()` 比较同一指针，恒等通过、零校验力）；
>    部署要**校验二进制哈希**（"服务就绪"≠"跑的是新代码"，本轮曾滞后 32 分钟）。
> 6. **"偶发失败"必须追到底**：一次偶发失败背后是产品代码并发 bug + 4 处测试竞争，
>    用 `-race` 检出。`go test` 不检测数据竞争，涉及并发的改动必须跑 `-race`。
> 7. **"无数据"必须与"错误"严格区分**：网关 `code=110` / 空 `data` 是**正常的成功响应**
>    （有的查询结果本身就是无数据），不是失败。响应会带 `_empty_result` 标记与说明，
>    聚合结果还会给出"N 个维度无记录"的顶层总结。**绝不能把错误响应也标成无数据**
>    （判定先排除 `error` 字段与非 100/110 的 code，有测试守护）。
>
> 参数表的第 4 列「说明」常含这类关键语义（如"精确匹配，母校置顶返回"），
> 提取器已保留（`doc_params[].note`）并计入 `business_params` 的 hint。

**全量审计**：`python3 eval/audit_param_conventions.py` 扫描 236 个接口的入参约定，
输出区划容器分布、混合声明接口、分页命名不一致、脏写键名等；
审计结论与实测证据台账见 `reports/param_conventions_map_20260917.md`。
**主体字段**（全量审计结论：`name` 81 / `entName` 19 / `companyName` 15，另有 text/keyword/chainHis）
由 `catalog.SubjectRequirementNote` 按接口自动推导并提示——传错主体字段时会返回警告说明本接口要求。

**已知上游限制**（实测于 2026-09-16，MCP 层已尽量标注/兜底，根因需上游修复）：

| 接口/行为 | 现象 | MCP 层处置 |
|---|---|---|
| 院校信息 | `entName` 被上游忽略（返回通用高校榜且计费）；`universityName` 为真实过滤参数 | 参数提示已标注"未按此过滤" |
| 校企筛选V2 | `searchKey` 返回裸 500；校友圈筛选真实用法为 `type=1`+`text`；`text` 过滤不严格；分页参数 `pageNo` **未在参数表中列出**（实测生效，`page` 才是无效名）；**区划须用 `regionList:[{province,city,county}]` 数组**，扁平 `province`/`county` 会被静默忽略且照常计费 | 提示与示例改为已验证用法；`school_alumni_circle` 做学历二次校验、通过 `page`/`max_pages` 暴露真实分页、通过 `province`/`city`/`county` 代为构造 regionList（缺 province 本地报错）；返回 `upstream_total`/`pages_scanned`/`_sampling` 声明扫描范围 |
| **区划参数约定不统一** | 各接口区划容器不同：`regionList` 数组（E_006_0021/E_007_1131）、`region` **对象**（E_007_1251）、扁平 `province`（E_007_1351/E_007_1171）、不生效（E_017_0081）；目录参数表普遍不声明真实容器参数——**传错写法会被静默忽略且照常计费** | 已逐接口实测并把正确写法固化到实现与回归测试（`region_conventions_test.go`）；E_007_1251 的静默失效 bug 已修（节点统计从 396 万→21 万才说明过滤真正生效） |
| 校企统计 | 原 `required_one_of` 误标 `name`，致"示例/必填/网关"三处互相矛盾 | 已修正为 `schoolName`（目录修正层，见 `internal/catalog/overrides.go`） |
| 107 | 语义为"账户未订阅该接口"（非"服务不存在"），未计费 | 错误文案已明确"未开通、未计费" |
| 法人学历 | 「查老板」的 `universityName` 为空；**但**「校企筛选V2」`companyId` 维度可返回企业人员学历（法人陈康康 = 哈尔滨工业大学，pId 与「主要人员」staffId 一致） | 已由 `school_alumni_circle` 的 `company_name` 入口收口，一步完成"企业→法人→院校→校友圈" |

### 尽调 `sections` 可选 key（26 维）

**基础（始终执行，3 节）**  
`basic`, `shareholders`, `key_personnel`

**司法（`include_judicial=false` 时跳过，12 节）**  
`court_filing`, `zhongben`, `judgment`, `enforced`, `dishonest`, `restraint`, `serious_dishonesty`, `court_announcement`, `judicial_auction`, `bankruptcy`, `court_hearing`, `judicial_assist`

**经营风险（不受 `include_judicial` 控制，11 节）**  
`admin_penalty`, `abnormal_operation`, `serious_illegal`, `liquidation`, `tax_violation`, `tax_arrears`, `equity_pledge`, `chattel_mortgage`, `tax_rating`, `public_sentiment`, `beneficial_owner`

**参数说明**

| 参数 | 默认 | 说明 |
|------|------|------|
| `sections` | 全开 26 节 | 逗号分隔子集 |
| `include_judicial` | `true` | `false` 跳过 12 个司法节 |
| `digest_only` | `false` | `true` 仅返回结构化摘要（`severity`/`risk_level`/`summary`），省 Token |
| `max_concurrency` | `4` | 并行请求数（1–9） |

**主体字段名（6 种）**

| 字段 | 适用接口类型 |
|------|--------------|
| `entName` | 工商、股东、人员、立案、限高、行政处罚、经营异常等 |
| `name` | 终本、被执行、失信、破产、税务违法、税务评级、进出口信用 |
| `text` | 裁判文书 |
| `companyName` | 供应商与客户、产品质量抽查 |
| `keyword` | 舆情检索、产业链搜索 |
| `chainHis` | 产业链节点 |

工具内部已按节映射；直调 `keeprisk_query` 前建议 `get_keeprisk_api(pcode)` 确认字段名。

### 聚合工具补充说明

- `enterprise_evaluation_report`：
  - 必须先锚定企业主体，`ent_name` / `company_id` 二选一。
  - 若未提供 `province`，仅执行 2 个企业维度接口，3 个行业地区维度会标记为 `skipped`。
  - 若提供 `city` / `county` / `economic_region`，必须同时提供 `province`。
- `industrial_park_search`：
  - 已知 `park_id` 时，可直接查询园区企业列表，最省调用次数。
  - 仅提供 `park_name` 时，工具会先查园区列表，再自动选择 `parkId` 后查询园区企业与园区分布。
  - 仅提供 `province` / `city` 时，执行地区筛选链路；与 `park_name` / `park_id` 可组合使用。

---

## Agent 工作流指南

`list_data_capabilities` 返回完整 **agent_playbook**（30+ 场景）。常见编排：

| 场景 | 推荐工具链 |
|------|------------|
| **A. 供应商准入** | `resolve_entity` → `enterprise_credit_score` → 评分≥80 轻量通过；50–79 标准尽调（`sections=basic,shareholders,admin_penalty,abnormal_operation,enforced,dishonest`）；<50 全量 + `control_penetration` |
| **B. 产业链招商** | `search_chains` → `get_chain_nodes` → `search_chain_enterprises` → `investment_readiness_score` → `enterprise_due_diligence_snapshot` |
| **C. 快速信用预审** | `resolve_entity` → `enterprise_credit_score` → 按 `risk_level` 分支（green/yellow/red） |
| **D. 上市公司分析** | `resolve_entity` → `listed_company_profile` → 尽调（`sections=basic,shareholders,key_personnel,equity_pledge`） |
| **E. 司法排查** | `resolve_entity` → 尽调（`include_judicial=true`, `digest_only=true`）→ 对 red 节查看原始数据 |
| **F. 供应链关联** | `supply_chain_relations` → 对高风险上下游逐一尽调 |
| **G. 成本控制** | 先 `digest_only=true` 扫描 → 命中风险再查原始数据；`include_judicial=false` 省 12 个司法节；调用前 `estimate_billing` |

**通用原则**：调尽调/穿透/实控人前，先 `resolve_entity` 确认主体；多候选时务必让用户选择。

---

## 资源

| URI | 说明 |
|-----|------|
| `keeprisk://catalog` | 全量最新接口目录（JSON，语义化字段：`title`/`product_line`/`category`；不含内部接口编码） |

---

## 鉴权与多租户

- 除 `/health`、`/ready` 外，所有请求必须带 **`Authorization: <token>`**（主推裸 token；也兼容标准 `Bearer <token>` 前缀，scheme 大小写不敏感）。
- 服务端经 `KEEPRISK_AUTH_MAP` 将 token 映射为 AppCode，网关调用仍以 Header `AppCode` 转发（映射对客户端透明）。
- 旧的 `AppCode`/`X-AppCode` 请求头**不再作为凭证**：仅携带它们（无 Authorization）→ **401**。
- **401** 也会写审计：成功记映射后 AppCode 的脱敏值；失败仅记 token 指纹（sha256 前 12 位）与失败类别，**完整 Authorization 绝不落盘**。
- 鉴权异常文案统一为「Authorization 无效或未登记」，不区分 scheme 错误与未登记，降低探测面。
- token 为映射表键，字符格式无限制（不含空格即可，空格会被识别为 scheme 分隔符）。

---

## 计费与尽调快照

- 每次成功到达网关的 `keeprisk_query` / 高层工具内单次 API 调用 ≈ **1 计费单位**（以网关 `charge` 为准）。
- 聚合工具内部并行多次 API（如 `enterprise_credit_score`=6 次、`listed_company_market`=15 次），响应含 `billing_units`。
- `enterprise_due_diligence_snapshot`：**每一节 = 1 次独立网关调用**；返回 `billing_units`。
- `estimate_billing`：**0 次网关**，按工具名与参数本地估算。
- `enterprise_evaluation_report`：未提供地区时估算 **2 次**；提供 `province` 后估算 **5 次**。
- `industrial_park_search`：按参数动态估算，`park_id` 约 **1 次**，`park_name` 约 **3 次**，地区筛选额外 **1 次**。
- 建议 Agent：先 `sections=basic,shareholders` 或 `include_judicial=false`，再按需加深；调用前可用 `estimate_billing` 预估。

**调用量核对**：`estimate_billing` 是调用前的**估算**；事后核对请用审计日志 `method=gateway` 的实测条数（逐 `pcode`）或工具层 `gateway_calls` 字段。缓存命中不计网关调用（`gateway_calls=0`）。

### 测试验证

```bash
go clean -testcache
go test ./...
```

在本轮修复完成后，上述命令已重新执行并通过。

---

## 运维与可观测性

- **优雅停机**：SIGINT/SIGTERM → `Shutdown` 30s → 冲洗审计队列。
- **`/ready`**：catalog 条数、缓存统计、时间戳；未就绪 **503**。
- **`X-Request-ID`**：每个受保护 HTTP 请求返回唯一 ID，关联 HTTP 层/工具层/网关层审计。
- **错误分类**（工具 JSON）：`validation` / `quota` / `config` / `api`，便于 Agent 重试或改参。
- **MCP 会话生命周期**：会话（`Mcp-Session-Id`）保存在进程内存中，**服务重启/热更新即全部失效**——重启后持旧会话 ID 的请求返回 404 `session not found`。这是 MCP 协议预期行为：客户端收到 404 后应**重新 `initialize` 建立新会话**，而非无限重试旧会话。运维上请尽量避开业务时段执行 `hot-reload.sh` / 容器重建。

### 审计日志三层结构

审计日志为 JSON Lines，同一 `request_id` 可串联三层记录：

| method | 粒度 | 关键字段 | 用途 |
|---|---|---|---|
| `http` | 每次传输层请求 | `tool="/mcp"`、`http_status` | 流量/鉴权（含 SSE 长连接与通知） |
| `tools/call` | 每次工具调用 | `tool`、`status`、`gateway_calls` | 工具使用与成本汇总 |
| `gateway` | 每次**网关 API 调用** | `pcode`、`tool`、`status`、`attempts` | 逐接口成本归因（计费定价） |

- `gateway_calls`（工具层字段）：该次工具调用实际发出的网关调用次数（成功失败均计），即计费单位还原依据；命中缓存为 0。
- `method=gateway`（网关层记录）：在网关调用唯一收口点 `keeprisk.Client.CallDataAPI` 落笔，因此**天然排除**缓存命中、本地校验失败与配额拒绝；聚合工具的内部扇出逐 `pcode` 可见（工具层只落顶层 `pcode_used`，聚合调用为空）。
- `attempts`：该次逻辑调用实际发出的 HTTP 尝试次数，`>1` 表示经历重试（网关实际被请求多次）。
- 开关 `KEEPRISK_AUDIT_PER_PCODE`：默认开启；设为 `false`/`0` 关闭逐接口明细（日志量敏感场景），此时工具层 `gateway_calls` 汇总仍保留，成本总量不丢失。

**定价说明**：早期版本需按扇出倍数估算网关调用量；当前可直接统计审计日志中 `method=gateway` 记录条数（逐接口实测值），无需额外脚本。

---

## Docker

```bash
docker compose up -d --build
```

镜像：多阶段构建 + `distroless` 非 root 运行。  
`docker-compose.yml` 中 healthcheck 已注释（distroless 无 shell/wget）；生产请用 K8s `httpGet` 探针或外部探活。

生产请通过 secrets / 配置中心注入 `KEEPRISK_API_BASE` 与 `KEEPRISK_AUTH_MAP`，勿将映射组明文写入镜像环境变量。

---

## Cursor / MCP 客户端配置

Streamable HTTP 示例（以 Cursor `mcp.json` 为准，字段名随客户端版本可能略有差异）：

```json
{
  "mcpServers": {
    "keeprisk-enterprise": {
      "url": "http://your-server:8080/mcp",
      "headers": {
        "Authorization": "<你的token>"
      }
    }
  }
}
```

> 也可写作标准格式 `"Authorization": "Bearer <你的token>"`，两种服务端均接受。

---

## 目录与接口元数据

- 运行期目录：**内嵌** `internal/catalog/data/full_catalog.json`（构建时 `//go:embed`），当前 **236** 条最新接口。
- 更新目录：将新生成的 `full_catalog.json` 同步到本仓库 `internal/catalog/data/` 与 `data/`（若保留副本），改动会随下次构建生效。

`go.mod` 模块路径为 `github.com/xrzlizheng/keeprisk-mcp`；fork 后如需推送自己的仓库，同步修改模块路径与各 import 即可。

---

## 开发构建

```bash
go mod tidy
go build -o keeprisk-mcp ./cmd/keeprisk-mcp
go test ./...
```

依赖：`github.com/modelcontextprotocol/go-sdk` v0.8.0。

---

## 网关状态码

| code | 含义 |
|------|------|
| 100 | 查询成功 |
| 110 | 成功，无记录 |
| 101 | 参数错误 |
| 103 | 账户不存在 |
| 104 | IP 限制 |
| 105 | 账号过期 |
| 107 | 服务不存在 |
| 108 | 通道关闭 |
| 109 | 资金不足 |
| 500 | 未知请求错误 |

工具输出已统一脱敏：`pcode_used` 等编码字段不再展示、文本中的编码字样以「〔内部接口〕」替代；旧编码自动升级（多版本重定向）仍在服务端生效，对调用方透明。

---

## 安全说明

- 查询主体 **不落明文审计**，仅 `subject_hash` + 长度。
- AppCode 审计为 **前缀脱敏**；Authorization **不落盘**（仅 sha256 指纹前 12 位 + 失败类别）。
- **对客输出统一脱敏**：接口编码、`base_url`/apikey 类凭证键、`KEEPRISK_*` 配置名、错误文案中的网关地址均不外泄（业务数据中的 URL 保留）；`KEEPRISK_HIDE_PCODE=false` 可临时关闭（启动告警，严禁生产）。
- 审计日志中的 `pcode`/`pcode_raw` 为监管留痕保留（审计不对客输出，二者不冲突）。
- 配额为 **滥用防护**，非计费系统；计费以网关为准。
- 生产务必 **HTTPS**（`KEEPRISK_REQUIRE_TLS=true`）。
- 缓存默认按 AppCode 隔离（跨租户共享曾存在伪造 AppCode 读旁路，P0）；`KEEPRISK_CACHE_PER_APPCODE=false` 可显式关闭（不推荐）。
- `KEEPRISK_AUTH_MAP`/`KEEPRISK_AUTH_MAP_FILE` 为过渡方案：生产经 secrets/配置中心注入；文件模式（`config/auth_map.json` + SIGHUP）支持授权增删热更新（会话不断），env 模式变更仍需重启进程。

---

## 相关文档

- [CHANGELOG.md](./CHANGELOG.md) — 变更记录（聚合工具计费修复、Agent-Native 六大域升级、反幻觉元数据引入）
- [USER_GUIDE.md](./USER_GUIDE.md) — 使用指南（工具详解与调用示例）
- [CONTRIBUTING.md](./CONTRIBUTING.md) — 贡献指南（构建、测试、凭证纪律）
- [SECURITY.md](./SECURITY.md) — 安全策略（凭证管理与漏洞报告）
- [eval/MCP_INSPECTOR_SCENARIOS.md](./eval/MCP_INSPECTOR_SCENARIOS.md) — MCP Inspector 手测场景（5 个 P2 验证场景：供应商准入/产业链招商/信用预审/成本控制/参数构建）

---

## 打赏支持

如果这个项目对你有帮助，欢迎请作者喝杯咖啡：

![微信收款码](docs/wechat_pay_qr.png)

---

## License

本项目采用 [MIT License](./LICENSE) 开源，可自由使用、修改与分发（保留版权声明）。
