# Dora：个人 AI Coding 用量 Demo 开发指南

> 状态：第一、二期实施基线
> 目标平台：macOS，本地单用户
> 范围：第一期 Codex 本地 Web 仪表盘；第二期 macOS 菜单栏与 Claude Code
> 不在本文范围：多 Agent session 管理、团队协作、排行榜、云端同步

## 1. 最终目标

这个 Demo 是一个完全本地运行的个人 AI Coding 用量仪表盘：

- 第一期以 Codex 为唯一正式支持的 usage provider。
- 实际 token 用量与订阅配额同等重要，但两条链路相互独立。
- 数据只存放在本机 SQLite，不需要 MySQL、PostgreSQL、Docker、登录系统或云服务。
- Web 页面只监听 `127.0.0.1`，由同一个 Go 核心进程提供 API 和静态资源。
- 第二期增加原生 macOS 菜单栏，并接入 Claude Code 的实际用量和订阅配额。
- 不保存 prompt、回复正文、工具参数或完整 transcript 副本。

推荐的两期产品形态如下：

| 阶段 | 产品形态 | Usage 数据 | Quota 数据 |
|---|---|---|---|
| 第一期 | 本地 Web 仪表盘 | Codex transcript | Codex OAuth quota |
| 第二期 | Web + macOS 菜单栏 | Codex + Claude Code transcript | Codex OAuth quota + Claude statusline quota |

## 2. 架构取舍

面向多用户部署的完整产品通常采用：

```text
本地 CLI parser
  → 30 分钟 bucket
  → HTTP ingest
  → PostgreSQL
  → 多用户聚合 API
  → React 页面 / 菜单栏
```

个人 Demo 改成：

```text
本地 provider parser
  → 统一 UsageEvent
  → 本地 SQLite
  → Go loopback API
  → React 页面 / SwiftUI 菜单栏
```

### 2.1 必须保留

- Provider 独立 parser。
- input、output、cache read、cache creation、reasoning 的分类口径。
- 上游 `total_tokens` 缺失明细时的保真处理。
- Codex fork、replay、subagent inherited history 去重。
- Claude Code stable message ID 与 streaming re-flush 去重。
- parser 版本升级后可重建本地数据。
- quota provider 的失败隔离、最后成功快照与 stale 状态。
- UTC 存储，以及所有统计组件共享同一个时间窗口。
- 本地文件只读、最小化持久化和日志脱敏。

### 2.2 第一、二期删除

- SSO、JWT、用户表、hostname 对账。
- HTTP ingest、full-sync staging、分批上传。
- PostgreSQL、TimescaleDB、物化视图。
- 团队、排行榜、组织维度和 canonical identity。
- 皮肤、成就、宠物、抽卡和分享卡。
- Skills、MCP、代码行数、自治度指标。
- 多设备同步和公网访问。
- 第三期的多 Agent session 管理。

## 3. 技术选型

### 3.1 核心进程

- Go。
- 标准库 `net/http` 提供本地 API 和静态文件。
- `modernc.org/sqlite` 作为纯 Go SQLite driver，避免个人构建时依赖 CGO。
- `go:embed` 内嵌前端构建产物。
- 单进程拥有 SQLite 写权限，Web 页面和菜单栏都通过 loopback API 访问数据。

个人 macOS Demo 直接固定纯 Go driver，减少构建分支。

### 3.2 Web

- React + TypeScript + Vite。
- 图表可以使用 Recharts。
- Vitest + Testing Library。
- 不需要 SSR、Node 服务端或复杂状态管理。

### 3.3 macOS 菜单栏

第二期使用一个薄的 SwiftUI `MenuBarExtra` App：

- 菜单栏 App 不直接读写 SQLite。
- 通过 `GET /api/v1/snapshot` 读取核心进程的规范化快照。
- 核心进程不可用时显示最后成功快照，并标记 stale。
- 通过 LaunchAgent 保持核心进程常驻。

选择 SwiftUI 外壳，是为了让 macOS UI 与 Go 数据核心保持清晰边界，同时避免把 AppKit 生命周期塞进统计代码。

## 4. 推荐目录结构

项目名为 `dora`：

```text
dora/
├── backend/
│   ├── cmd/
│   │   └── dora/
│   │       └── main.go
│   ├── internal/
│   │   ├── app/
│   │   │   ├── runtime.go
│   │   │   └── scheduler.go
│   │   ├── config/
│   │   │   └── config.go
│   │   ├── domain/
│   │   │   ├── usage.go
│   │   │   ├── quota.go
│   │   │   └── window.go
│   │   ├── provider/
│   │   │   ├── provider.go
│   │   │   ├── codex/
│   │   │   │   ├── discover.go
│   │   │   │   ├── parser.go
│   │   │   │   ├── lineage.go
│   │   │   │   ├── dedup.go
│   │   │   │   └── quota.go
│   │   │   └── claude/
│   │   │       ├── discover.go
│   │   │       ├── parser.go
│   │   │       ├── dedup.go
│   │   │       ├── statusline.go
│   │   │       └── hook_install.go
│   │   ├── scan/
│   │   │   ├── scanner.go
│   │   │   ├── checkpoint.go
│   │   │   └── rebuild.go
│   │   ├── storage/
│   │   │   └── sqlite/
│   │   │       ├── db.go
│   │   │       ├── migrations.go
│   │   │       ├── usage_repository.go
│   │   │       └── quota_repository.go
│   │   ├── analytics/
│   │   │   ├── summary.go
│   │   │   ├── timeline.go
│   │   │   └── breakdown.go
│   │   └── httpapi/
│   │       ├── server.go
│   │       ├── usage_handlers.go
│   │       ├── quota_handlers.go
│   │       └── diagnostics_handlers.go
│   ├── migrations/
│   └── testdata/
│       ├── codex/
│       └── claude/
├── frontend/
│   ├── web/
│   │   ├── src/
│   │   └── vite.config.ts
│   └── macos/
│       └── DoraMenu/
├── docs/
│   └── dora-development-guide.md
└── Makefile
```

`backend/` 保存 Go 核心、数据库 migration 和 provider 测试数据；`frontend/` 保存 Web 与 macOS 两种界面；`docs/` 保存设计和开发文档。前端只能通过 loopback API 使用后端能力，不直接读取 SQLite 或解析 Agent 原始文件。

不要把所有 provider 放进一个超大 parser 文件。共享的是领域模型和扫描协议，不是每家上游的 JSON 格式。

## 5. Provider 接口

实际用量与订阅配额是两类不同能力，不要强迫一个接口同时实现两者：

```go
type UsageProvider interface {
    ID() string
    Discover(ctx context.Context) ([]SourceFile, error)
    Parse(ctx context.Context, file SourceFile, checkpoint *Checkpoint) (ParseResult, error)
}

type QuotaProvider interface {
    ID() string
    FetchQuota(ctx context.Context) QuotaResult
}
```

设计约束：

- Provider 失败只能影响自己。
- Quota 失败不能阻止 usage 扫描。
- Usage 失败不能清空最后一次成功 quota。
- 所有错误必须包含 provider、文件路径和当前操作。
- Provider ID 使用明确值：`provider.codex`、`provider.claude-code`。

## 6. 统一领域模型

### 6.1 UsageEvent

```go
type UsageEvent struct {
    Source                    string
    DedupKey                  string
    OccurredAt                time.Time
    Model                     string
    Project                   string

    InputTokens               int64
    OutputTokens              int64
    CachedInputTokens         int64
    CacheCreationInputTokens  int64
    ReasoningOutputTokens     int64

    ReportedTotalTokens       int64
    TotalTokens               int64

    RolloutKey                string
    ParentRolloutKey          string
    ReplayFingerprint         string
    InheritedReplay           bool
}
```

字段语义：

- `InputTokens` 是归一化后的普通输入，不包含 cache read 和 cache creation。
- `OutputTokens` 是 provider 归一化后的普通输出。
- cache read、cache creation、reasoning 永远分列。
- `ReportedTotalTokens` 保存上游报告值，用于明细不完整时保真。
- `TotalTokens` 是 UI 和统计使用的最终口径。
- `DedupKey` 只用于本地去重，不暴露给 UI。

### 6.2 总 token

统一计算：

```go
detailTotal :=
    input +
    output +
    cachedInput +
    cacheCreationInput +
    reasoningOutput

total := max(reportedTotal, detailTotal)
```

采用“报告总量与明细之和取最大值”的防丢失策略。Codex 可能只报告 `total_tokens` 而明细为零，这种记录不能丢弃。

### 6.3 QuotaSnapshot

```go
type QuotaSnapshot struct {
    Provider          string
    WindowKey         string
    Label             string
    UsedPercent       float64
    RemainingPercent  float64
    ResetsAt          *time.Time
    FetchedAt         time.Time
    Source            string
    SourceState       string
    Plan              string
}
```

`SourceState` 取值：

- `confirmed`
- `stale`
- `unsupported`
- `not_configured`
- `error`

只有成功获取到数值的记录进入 quota 历史表；失败状态进入 provider 状态表，不能用失败记录覆盖最后一次成功值。

## 7. SQLite 设计

### 7.1 文件位置

遵循 macOS 本地应用目录：

```text
~/Library/Application Support/Dora/dora.db
~/Library/Application Support/Dora/settings.json
~/Library/Application Support/Dora/inbox/
~/Library/Logs/Dora/dora.log
```

首次创建时：

- 目录权限 `0700`。
- 数据库、设置和 quota inbox 文件权限 `0600`。
- 不把数据库放进项目目录。

### 7.2 SQLite 初始化

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
```

核心进程设置 `SetMaxOpenConns(1)`，让所有写事务串行化。读取可以通过同一进程的 repository 层完成。

### 7.3 最小 schema

```sql
CREATE TABLE schema_migrations (
    version       INTEGER PRIMARY KEY,
    applied_at_ms INTEGER NOT NULL
);

CREATE TABLE scan_runs (
    run_id          TEXT PRIMARY KEY,
    source          TEXT NOT NULL,
    mode            TEXT NOT NULL,
    started_at_ms   INTEGER NOT NULL,
    finished_at_ms  INTEGER,
    status          TEXT NOT NULL,
    files_seen      INTEGER NOT NULL DEFAULT 0,
    events_seen     INTEGER NOT NULL DEFAULT 0,
    error_message   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE source_files (
    source             TEXT NOT NULL,
    path               TEXT NOT NULL,
    file_identity      TEXT NOT NULL DEFAULT '',
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    mtime_ns           INTEGER NOT NULL DEFAULT 0,
    parsed_offset      INTEGER NOT NULL DEFAULT 0,
    complete_line_end  INTEGER NOT NULL DEFAULT 0,
    head_hash          TEXT NOT NULL DEFAULT '',
    tail_hash          TEXT NOT NULL DEFAULT '',
    parser_version     INTEGER NOT NULL,
    parser_state_json  TEXT NOT NULL DEFAULT '',
    last_success_at_ms INTEGER,
    last_error         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (source, path)
);

CREATE TABLE usage_events (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    source                      TEXT NOT NULL,
    dedup_key                   TEXT NOT NULL,
    occurred_at_ms              INTEGER NOT NULL,
    model                       TEXT NOT NULL,
    project                     TEXT NOT NULL DEFAULT 'unknown',
    input_tokens                INTEGER NOT NULL DEFAULT 0,
    output_tokens               INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_output_tokens     INTEGER NOT NULL DEFAULT 0,
    reported_total_tokens       INTEGER NOT NULL DEFAULT 0,
    total_tokens                INTEGER NOT NULL DEFAULT 0,
    rollout_key                 TEXT NOT NULL DEFAULT '',
    parent_rollout_key          TEXT NOT NULL DEFAULT '',
    replay_fingerprint          TEXT NOT NULL DEFAULT '',
    inherited_replay            INTEGER NOT NULL DEFAULT 0,
    updated_at_ms               INTEGER NOT NULL,
    UNIQUE (source, dedup_key)
);

CREATE INDEX idx_usage_events_time
    ON usage_events (occurred_at_ms);

CREATE INDEX idx_usage_events_source_time
    ON usage_events (source, occurred_at_ms);

CREATE INDEX idx_usage_events_model_time
    ON usage_events (model, occurred_at_ms);

CREATE INDEX idx_usage_events_project_time
    ON usage_events (project, occurred_at_ms);

CREATE TABLE usage_events_staging (
    run_id                      TEXT NOT NULL,
    source                      TEXT NOT NULL,
    dedup_key                   TEXT NOT NULL,
    occurred_at_ms              INTEGER NOT NULL,
    model                       TEXT NOT NULL,
    project                     TEXT NOT NULL DEFAULT 'unknown',
    input_tokens                INTEGER NOT NULL DEFAULT 0,
    output_tokens               INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_output_tokens     INTEGER NOT NULL DEFAULT 0,
    reported_total_tokens       INTEGER NOT NULL DEFAULT 0,
    total_tokens                INTEGER NOT NULL DEFAULT 0,
    rollout_key                 TEXT NOT NULL DEFAULT '',
    parent_rollout_key          TEXT NOT NULL DEFAULT '',
    replay_fingerprint          TEXT NOT NULL DEFAULT '',
    inherited_replay            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, source, dedup_key)
);

CREATE TABLE quota_snapshots (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    provider           TEXT NOT NULL,
    window_key         TEXT NOT NULL,
    label              TEXT NOT NULL,
    used_percent       REAL NOT NULL,
    remaining_percent  REAL NOT NULL,
    resets_at_ms       INTEGER,
    fetched_at_ms      INTEGER NOT NULL,
    source             TEXT NOT NULL,
    source_state       TEXT NOT NULL,
    plan               TEXT NOT NULL DEFAULT '',
    UNIQUE (provider, window_key, fetched_at_ms)
);

CREATE INDEX idx_quota_provider_time
    ON quota_snapshots (provider, fetched_at_ms DESC);

CREATE TABLE provider_state (
    provider            TEXT PRIMARY KEY,
    usage_status        TEXT NOT NULL DEFAULT 'not_scanned',
    quota_status        TEXT NOT NULL DEFAULT 'not_configured',
    last_usage_at_ms    INTEGER,
    last_quota_at_ms    INTEGER,
    last_usage_error    TEXT NOT NULL DEFAULT '',
    last_quota_error    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE model_prices (
    model_pattern                       TEXT PRIMARY KEY,
    display_name                        TEXT NOT NULL,
    input_price_per_mtok_usd            REAL NOT NULL,
    output_price_per_mtok_usd           REAL NOT NULL,
    cache_read_price_per_mtok_usd       REAL NOT NULL,
    cache_creation_price_per_mtok_usd   REAL NOT NULL,
    reasoning_price_per_mtok_usd        REAL NOT NULL,
    source                              TEXT NOT NULL,
    updated_at_ms                       INTEGER NOT NULL
);
```

### 7.4 为什么保存事件而不是只保存 bucket

30 分钟 bucket 适合多人和大规模聚合。个人版保存去重后的事件更合适：

- parser 修正后可以重建并核对单条事件。
- 定价表变化时可以重新计算费用。
- 可以检查 reported total 与明细差异。
- 仍可通过 SQL 或 Go 聚合出 30 分钟和日级趋势。

不要保存原始 JSON 行。`source_files` 保存路径和 checkpoint，`usage_events` 只保存统计元数据。

## 8. 时间窗口必须统一

所有数据以 UTC 毫秒保存。API 层创建唯一的 `TimeWindow`：

```go
type TimeWindow struct {
    StartUTC time.Time
    EndUTC   time.Time
    Location *time.Location
    Range    string
}
```

规则：

- 查询使用 `[start, end)`。
- Web 统一使用 1D、7D、30D、ALL；历史兼容输入只允许在统一时间窗口函数中处理。
- 第一期用量统计统一始于 `2026-07-29`，任何范围都不能读取更早的数据。
- 统计起始日是产品口径，应使用具名常量集中维护，并由 API 返回给前端，不能在多个页面重复写死。
- 1D、7D、30D、ALL 的边界由一个函数生成。
- summary、timeline、model breakdown、project breakdown 必须复用同一个 `TimeWindow`。
- 日级 label 在用户的 macOS 时区中生成。
- 不允许每个 handler 自己计算 `time.Now()`。

时间窗口不统一会造成 headline token 与 daily trend 合计不一致。Dora 应直接把“summary tokens 等于 timeline tokens 之和”写成回归测试。

# 第一期：Codex 本地 Web 仪表盘

## 9. Codex 文件发现

默认扫描：

```text
~/.codex/sessions/
~/.codex/archived_sessions/
```

配置优先级：

1. 应用设置中的 Codex home 列表。
2. `CODEX_HOME`。
3. `~/.codex`。

发现规则：

- 递归查找 `.jsonl`、`.jsonl.gz`、`.jsonl.zst`。
- 路径去重并排序，保证扫描结果确定性。
- 缺失目录不是错误。
- 权限错误需要进入 diagnostics。
- 压缩文件只支持全量解析，不做 offset append。

## 10. Codex parser

### 10.1 需要识别的记录

#### `session_meta`

用途：

- 从 `payload.cwd` 获取项目名。
- 从 `payload.id` 获取稳定 rollout ID。
- 获取父子 lineage。

项目默认只保存 `filepath.Base(cwd)`，避免把完整用户名和本地目录暴露到 Web API。

Lineage 父 ID 的候选顺序：

1. `payload.parent_thread_id`
2. `payload.forked_from_id`
3. `payload.source.subagent.thread_spawn.parent_thread_id`

如果出现多个互相冲突的父 ID：

- 标记 lineage ambiguous。
- 不进行危险的 inherited replay 删除。
- 保留事件并在 diagnostics 中提示。

Rollout key 由“配置 home + rollout ID”生成后再哈希，不能只用 rollout ID，也不能跨不同 Codex home 去重 lineage。

#### `turn_context`

用途：

- 更新当前 turn 的模型。
- 关闭子 rollout 的 inherited replay prefix。

即使 `turn_context` 没有时间戳或时间戳无效，也必须关闭 inherited replay prefix。时间戳只决定它能否形成带时间事件，不能决定 lineage 边界。

#### `event_msg/token_count`

只处理：

```text
type == "event_msg"
payload.type == "token_count"
payload.info != nil
```

优先使用：

```text
payload.info.last_token_usage
```

如果缺失，再回退：

```text
payload.info.total_token_usage
```

不要把每条累计 `total_token_usage` 都直接相加。

### 10.2 Codex token 归一化

Codex 的 token 归一化是 provider-specific 的：

```go
inputRaw := usage.input_tokens
outputRaw := usage.output_tokens
cached := usage.cached_input_tokens + usage.cache_read_input_tokens
cacheCreation := usage.cache_creation_input_tokens
reasoning := usage.reasoning_output_tokens

cached = min(cached, inputRaw)
cacheCreation = min(cacheCreation, inputRaw-cached)
reasoning = min(reasoning, outputRaw)

input := max(0, inputRaw-cached-cacheCreation)
output := max(0, outputRaw-reasoning)
```

这一步不可省略，否则 cache 和 reasoning 会同时计入普通 input/output，造成双计。

所有 token 字段：

- 必须是非负 `int64`。
- 超出范围或 JSON 数字格式异常时记录 parser error。
- 不能因为某个字段缺失就丢弃整条合法 `total_tokens`。

### 10.3 模型和项目回填

- 初始模型为 `unknown`。
- `payload.info.model` 优先。
- 缺失时使用当前 `turn_context.model`。
- token event 早于第一个 `turn_context` 时，可以在无父 lineage 的 rollout 中使用后来发现的确定模型回填。
- 有父 lineage 的 inherited prefix 不做盲目模型回填，否则会妨碍 parent/child replay 对账。

### 10.4 超大 JSONL 行

Codex 的 patch 输出可能产生超大 JSONL 行。对于能够确认属于 patch apply output 的超大记录，可以安全跳过并继续解析后面的 token event；其他未知超大记录必须报错。

第一期至少应：

- 设置单行上限。
- 对明确、可证明不含 token usage 的 patch output 提供安全跳过。
- 对未知超大行停止该文件解析并保留旧数据。
- 不因一次解析失败执行 source replace。

## 11. Codex 去重

### 11.1 基础 dedup key

Codex token event 没有可靠的 message ID。Dedup key 不包含本地时间戳、文件路径或 session ID：

```text
provider.codex
+ model
+ normalized last input/output/cache/cache-create/reasoning
+ total usage input/output/cache/cache-create/reasoning/total
+ last usage total_tokens
```

对拼接后的规范字符串计算 SHA-256，保存 128 bit 或完整 hash。

为什么不使用时间戳：

- fork 或 subagent replay 可能重写本地时间戳。
- usage tuple 和 running total 保持一致。
- 时间戳进入 key 会让 replay 穿过去重。

### 11.2 Largest-wins

同一个 dedup key 出现多条记录时：

- 比较五类 token 明细之和。
- 保留 token 更完整的一条。
- 如果相同，使用确定性的 first-wins。

这是为 streaming re-flush 和零 usage 中间记录准备的统一行为。

### 11.3 Fork 与 inherited replay

基础 dedup 能处理完整复制，但 Codex Desktop 子代理可能出现：

- 父记录模型已知。
- 子记录 inherited prefix 模型为 `unknown`。
- usage 和 running total 相同。

因此增加 model-agnostic `ReplayFingerprint`：

```text
normalized last usage
+ complete total usage
```

只有 `total_token_usage` 至少包含 `input_tokens`、`output_tokens`、`total_tokens` 时才能生成 replay fingerprint。

删除 inherited replay 的条件必须同时满足：

- 子事件位于第一个 `turn_context` 之前。
- 子 rollout 有唯一、可解析的 parent lineage。
- ancestor closure 完整。
- ancestor 中存在相同 replay fingerprint 的非 inherited 事件。
- 不跨 Codex home。

任一条件不满足时选择保留，宁可轻微高估，也不能静默丢失真实 child usage。

### 11.4 必须有的去重测试

- 整个 rollout 被 fork-copy，只计算一次。
- 同文件重复同一 `token_count`，只计算一次。
- replay 改写时间戳，历史段只计算一次。
- 子代理模型未知、父事件模型已知，历史段归父事件。
- 子代理存在真实的新 usage，新 usage 被保留。
- 父文件缺失时不删除 unknown child usage。
- lineage cycle 或冲突时不删除 usage。
- 不同 Codex home 不互相去重。
- 同一 dedup key 的零 usage 与完整 usage，保留完整 usage。

## 12. 扫描与重建

### 12.1 正确性优先的全量扫描

第一版先实现 source 级原子重建：

1. 创建 `scan_runs` 记录。
2. 扫描全部 Codex 文件。
3. 结果写入 `usage_events_staging(run_id, ...)`。
4. 在 staging 内完成全局 dedup 和 lineage reconciliation。
5. 所有必需文件都成功后，开启短事务：
   - 删除 `usage_events WHERE source='provider.codex'`。
   - 从 staging 插入新 generation。
   - 更新 `source_files`。
   - 标记 scan run 成功。
6. 提交后删除 staging 数据。

如果任何必需 parser 失败：

- 不替换旧 generation。
- 保留上次成功数据。
- provider 状态设为 degraded/error。
- Web 页面展示错误和旧数据新鲜度。

### 12.2 增量扫描

全量正确后再增加：

- 未变化文件直接跳过。
- 普通 JSONL 只在 identity、head、tail 和旧 offset 都匹配时 append parse。
- 只解析最后一个完整换行之前的数据。
- 文件截断、替换、头尾 hash 变化时触发 source 全量重建。
- parser version 变化时触发 source 全量重建。
- `.gz`、`.zst` 变化时触发该 source 全量重建。
- 每天安排一次低优先级全量校验，修复删除文件造成的陈旧事件。

增量扫描使用 file identity、size、mtime、offset、head/tail hash、parser state 和版本号。个人版只需要本地 source rebuild，不需要远程 full-sync upload 状态机。

### 12.3 并发

第一期只支持一个正式 provider，不需要并行 parser。仍需实现：

- process 内 singleflight，避免手动刷新和定时刷新重叠。
- SQLite `BEGIN IMMEDIATE` 写事务。
- Web 查询不等待长时间文件扫描。
- 扫描在 staging 完成后再执行短 swap。

## 13. Codex 订阅配额

### 13.1 数据来源

读取候选：

```text
$CODEX_HOME/auth.json
~/.codex/auth.json
```

只接受 OAuth subscription：

- `tokens.access_token`
- `tokens.account_id`
- `tokens.id_token` 只用于生成脱敏 account label

仅有 `OPENAI_API_KEY` 时返回 `unsupported`，不尝试推断订阅额度。

### 13.2 网络边界

Codex quota 是第一期唯一允许的 provider 网络访问：

- 设置页明确展示并要求用户开启“读取 Codex 订阅配额”。
- access token 只保存在函数局部内存。
- 不写 SQLite、不写 settings、不写日志。
- URL hard-code 到允许的官方域名，不能被环境变量改写。
- 连接与总请求均设置超时。
- 不发送 prompt、usage event 或本地文件信息。

Dora 按以下顺序尝试：

```text
https://chatgpt.com/backend-api/wham/usage
https://chatgpt.com/api/codex/usage
```

这属于可能变化的上游接口。实现时必须把 HTTP transport 与 JSON normalization 封装在 `provider/codex/quota.go`，并用 fixture 测试，不能让 endpoint shape 泄漏到 UI。

### 13.3 窗口识别

不要假设 `primary_window` 永远是 5h、`secondary_window` 永远是 7d。

按 `limit_window_seconds` 识别：

```text
5h = 5 * 60 * 60
7d = 7 * 24 * 60 * 60
```

规范化：

```go
remaining := clamp(100-used, 0, 100)
resetsAt := time.Unix(resetAt, 0).UTC()
```

### 13.4 刷新与缓存

- 默认每 5 分钟最多请求一次。
- 手动刷新也经过 singleflight。
- 401/403 显示重新运行 `codex login`。
- 404 时才尝试 fallback endpoint。
- 网络失败保留最后一次成功 quota。
- 超过 10 分钟未成功更新标记 stale。
- quota 失败不能影响 transcript token 统计。

## 14. 可选费用估算

第一期可以展示费用，但它是 token 的派生值：

```text
input / 1M × input price
+ output / 1M × output price
+ cache read / 1M × cache read price
+ cache creation / 1M × cache creation price
+ reasoning / 1M × reasoning price
```

要求：

- 定价表带来源和更新时间。
- 最长、最具体的 model pattern 优先匹配。
- 未匹配模型显示“未定价”。
- 不使用一个任意默认价格静默估算未知模型。
- 更新定价不需要重扫 transcript。

## 15. 本地 API

所有 API 使用 `/api/v1`：

| Method | Path | 用途 |
|---|---|---|
| GET | `/health` | 进程、数据库和 schema 状态 |
| GET | `/summary?range=7D` | 总 token、分项、cache rate、可选费用 |
| GET | `/timeline?range=30D&granularity=day` | 趋势 |
| GET | `/breakdown?range=30D&dimension=model` | 模型分布 |
| GET | `/breakdown?range=30D&dimension=project` | 项目分布 |
| GET | `/quotas` | 最新 quota 与 stale 状态 |
| GET | `/snapshot` | Web 和第二期菜单栏共用的紧凑快照 |
| GET | `/diagnostics` | 数据源、扫描状态、错误 |
| POST | `/scan` | 手动触发扫描 |
| POST | `/quota/refresh` | 手动刷新 quota |
| GET/PUT | `/settings` | 本地设置 |

安全要求：

- 只监听 `127.0.0.1`，不监听 `0.0.0.0`。
- 前后端同源，不开放 CORS。
- 写接口验证 `Origin`。
- 启动时生成随机 control token，写接口要求 header。
- API 不返回 `source_files.path`、access token 或原始错误 body。
- JSON 错误包含 provider、操作和可行动建议。

## 16. Web 仪表盘

### 16.1 页面

第一期只需要两个页面：

#### Dashboard

- 1D / 7D / 30D / ALL。
- 总 token。
- 普通输入、cache read、cache creation、普通输出、reasoning。
- cache 命中率：

```text
cache read / (input + cache read + cache creation)
```

- 每日趋势堆叠图。
- GitHub 风格的 53 周 Token 热力图，明确展示统计起始日；热力图通过统一 API 获取完整活动数据，不跟随当前汇总范围截断。
- 模型分布。
- 项目分布。
- 可选费用。
- Codex 5h/7d quota 卡片。

#### Diagnostics / Settings

- Codex home。
- 最近扫描时间。
- 扫描文件数、事件数。
- parser version。
- quota consent。
- quota 最后成功时间。
- 错误与修复建议。
- “全量重建”按钮。

### 16.2 空态与错误态

- 没有 Codex 目录：展示安装/运行 Codex 的提示。
- 有目录但没有 token event：展示扫描路径和文件数。
- quota 未启用：usage 正常显示，quota 卡片显示未启用。
- quota 请求失败：显示最后成功值和 stale badge。
- parser 部分失败：显示旧数据，不显示虚假的全零。

### 16.3 Snapshot DTO

Web 和菜单栏共用：

```json
{
  "generatedAt": "2026-07-30T12:00:00Z",
  "usage": {
    "todayTokens": 123456,
    "sevenDayTokens": 765432,
    "allTimeTokens": 9876543,
    "topModel": "gpt-5.6-sol",
    "lastScanAt": "2026-07-30T11:59:20Z",
    "stale": false
  },
  "quotas": [
    {
      "provider": "provider.codex",
      "windowKey": "five_hour",
      "usedPercent": 42,
      "remainingPercent": 58,
      "resetsAt": "2026-07-30T15:00:00Z",
      "sourceState": "confirmed"
    }
  ],
  "errors": []
}
```

## 17. 第一期运行方式

CLI：

```text
dora serve
dora scan
dora scan --full
dora quota refresh
dora doctor
```

`dora serve`：

1. 初始化目录和 SQLite。
2. 运行 migration。
3. 启动 loopback server。
4. 异步启动一次 usage scan。
5. quota 已授权时异步刷新。
6. 默认打开浏览器。
7. 每 5 分钟检查增量 usage 和 quota。

不要在 HTTP server ready 之前执行耗时全量扫描。

## 18. 第一期测试与验收

### 18.1 测试约束

- 所有 parser 测试使用 `t.TempDir()`。
- 不读取开发者真实 `~/.codex`。
- quota HTTP 使用注入 fetcher 和本地 fixture。
- 测试不得访问网络。
- testdata 不包含真实 prompt、用户名、邮箱、token 或项目路径。

### 18.2 必须覆盖

#### Parser

- 基础 Codex token event。
- cache/reasoning 归一化。
- 仅 `reported total` 有值。
- token event 早于 model context。
- gzip/zstd。
- 不完整末行。
- 安全超大 patch 跳过。
- 未知超大行失败并保留旧 generation。

#### Dedup

- 第 11.4 节所有 fork/replay 用例。
- 同一输入重复全量扫描，SQLite 总量不变。

#### Storage

- staging 失败不替换 active generation。
- migration 重复执行幂等。
- WAL 与并发读取。
- 非负约束在 repository 层生效。

#### Analytics

- summary total 等于五类明细和 `reported total` 的保真结果。
- summary token 等于 timeline token 之和。
- 1D 在本地时区午夜正确切换。
- 所有时间窗口都不会早于 `2026-07-29`。
- DST 切换日不重复或漏计。

#### Quota

- 5h/7d 按 duration 识别。
- primary/secondary 交换不影响语义。
- 404 fallback。
- 401/403。
- stale fallback。
- access token 不出现在日志、SQLite 和返回 JSON。

### 18.3 第一期 Definition of Done

- 在用户 Mac 上一条命令启动。
- 首次扫描能展示 Codex 历史 token。
- 重复扫描结果完全幂等。
- fork fixture 不重复计数。
- input/cache/output/reasoning 口径可解释。
- Codex quota 开启后展示 5h/7d 和重置时间。
- quota 离线时 usage 仍可用。
- SQLite 不包含 prompt 或 access token。
- Web 只通过 loopback 访问。
- Diagnostics 能解释每个失败 provider。

# 第二期：macOS 菜单栏与 Claude Code

## 19. 第二期架构

```text
                         ┌──────────────────┐
Codex / Claude files ──→ │ Go Core + SQLite │
Claude statusline ─────→ │ + /api/v1       │
                         └────────┬─────────┘
                                  │ loopback JSON
                         ┌────────▼─────────┐
                         │ SwiftUI MenuBar  │
                         └──────────────────┘
```

原则：

- SQLite 仍然只有 Go Core 一个写入者。
- SwiftUI 不复制 analytics SQL。
- Web 和菜单栏使用同一个 snapshot DTO。
- Provider 独立失败。

## 20. macOS 菜单栏

### 20.1 功能

菜单栏标题默认显示今日 token，例如：

```text
12.4M
```

Popover：

- 1D / 7D / ALL token。
- top model。
- Codex 5h/7d quota。
- Claude Code 5h/7d quota。
- 最后扫描和刷新时间。
- stale/error badge。
- “立即刷新”。
- “打开仪表盘”。
- “设置”。
- “退出”。

不在菜单栏实现复杂趋势图和项目表格。

### 20.2 刷新

- 打开 popover 时读取 snapshot。
- 后台每 5 分钟刷新。
- 用户手动刷新调用 Core API，不在 SwiftUI 内自己扫描文件。
- Core 对 scan/quota refresh 使用 singleflight。
- API 失败时保留最后成功 snapshot，标记 stale。

### 20.3 LaunchAgent

第二期提供显式命令：

```text
dora service install
dora service uninstall
dora service status
```

安装内容：

- `~/Library/LaunchAgents/<bundle-id>.core.plist`
- Core 日志指向 `~/Library/Logs/Dora/`
- `RunAtLoad=true`
- `KeepAlive` 仅对异常退出生效

要求：

- plist 中使用绝对 binary path。
- 安装前验证 binary 存在。
- 重复安装幂等。
- uninstall 不删除 SQLite 数据。
- 菜单栏退出与 Core service 停止是两个不同操作。

## 21. Claude Code 文件发现

默认扫描：

```text
~/.claude/projects/**/*.jsonl
```

配置优先级：

1. 应用设置中的 Claude config dir。
2. `CLAUDE_CONFIG_DIR`。
3. `~/.claude`。

主 transcript：

```text
~/.claude/projects/<project>/<session>.jsonl
```

subagent transcript：

```text
~/.claude/projects/<project>/<session>/subagents/agent-*.jsonl
```

缺失目录不是错误。第二期不适配 CAC、Relay、Seed 等 Claude-compatible 变体。

## 22. Claude Code parser

### 22.1 Assistant usage

只从 assistant message 的 `message.usage` 读取 token：

- `input_tokens`
- `output_tokens`
- `cache_read_input_tokens`
- `cache_creation_input_tokens`
- `reasoning_output_tokens`

模型来自 `message.model`，缺失时为 `unknown`。

Claude 与 Codex 的归一化不能共用同一套减法：

- Claude 的 cache read/cache creation 是独立字段。
- 原生 `reasoning_output_tokens` 存在时按上游独立字段保留。
- 原生 reasoning 缺失时，对 Anthropic 模型按 thinking/other 字符比例从 output 中估算 reasoning。

第二期实现 thinking carve：

1. 同一 stable message ID 聚合 thinking block 与其他输出字符数。
2. `reasoning = output × thinkingChars / totalChars`。
3. 从普通 output 中减去估算 reasoning。
4. 原生 reasoning 非零时绝不重复估算。
5. 非 Anthropic 模型不估算。
6. 只在内存读取字符数，不保存文本。

### 22.2 Stable ID 与 largest-wins

Dedup key 顺序：

1. `message.id`
2. 本地 record `uuid`
3. 内容指纹

命名空间：

```text
provider.claude-code:<stable-id>
```

Claude Code 可能用同一个 message ID 重复 flush：

- 前几条 usage 全零。
- 最后一条带完整 token。

同 key 仍然采用 largest-wins。

### 22.3 Fork

Claude fork 复制历史消息时，`message.id` 通常保持不变。Dedup key 不能包含：

- session ID
- 文件路径
- project

这样主会话与 fork 文件中的相同历史消息只计算一次。

### 22.4 Subagent

- subagent token 是真实用量，必须计入。
- subagent 文件独立参与文件 checkpoint。
- subagent usage 使用自己的 stable message ID 去重。
- 第二期不把 subagent 作为独立可管理 session。
- project 继承父目录。

## 23. Claude Code 配额

### 23.1 首选 statusline hook

首选方式不是读取 Claude credential，而是利用 Claude Code 的 `statusLine` stdin：

```json
{
  "rate_limits": {
    "five_hour": {
      "used_percentage": 30,
      "resets_at": 1785400000
    },
    "seven_day": {
      "used_percentage": 61,
      "resets_at": 1785800000
    }
  }
}
```

Hook：

```text
dora hook claude-statusline
```

行为：

1. 从 stdin 读取 JSON。
2. 只提取 rate limit。
3. 原子写入：

```text
~/Library/Application Support/Dora/inbox/claude-statusline.json
```

4. 文件权限 `0600`。
5. 不访问网络。
6. 不读取 credential。
7. 尽快退出，失败也不阻断 Claude Code。

Core 定时读取 inbox，规范化后写入 `quota_snapshots`。

### 23.2 Hook 安装

显式命令：

```text
dora claude-hook install
dora claude-hook status
dora claude-hook uninstall
```

要求：

- 只有用户运行 `install` 才修改 `~/.claude/settings.json`。
- 修改前验证 JSON。
- 使用临时文件 + rename 原子写入。
- 保留原有 statusLine 配置，不能静默覆盖。
- app-owned settings 中保存原配置的结构化备份。
- uninstall 只恢复自己安装时保存的原值。
- 如果当前配置已被用户再次修改，uninstall 停止并提示冲突，不强行覆盖。
- hook 输出无 quota 时不覆盖最后成功 snapshot。

### 23.3 新鲜度

Claude quota 只有 Claude Code 正在运行并触发 statusline 时才更新：

- 5 分钟内：confirmed。
- 超过 5 分钟：stale。
- hook 已安装但没有 snapshot：not_running。
- hook 未安装：not_configured。

不要把“Claude 没运行”显示成 usage parser 错误。

### 23.4 可选 API fallback

若未来需要主动刷新，可以提供显式环境变量：

```text
DORA_CLAUDE_OAUTH_TOKEN
```

再调用 Claude quota API。此 fallback 不作为第二期验收必需项：

- 不从 Keychain、cookie 或 Claude credential 文件自动提取。
- token 不落盘。
- 用户必须显式配置。
- statusline 数据优先。

## 24. 第二期测试与验收

### 24.1 macOS 菜单栏

- Core 可用时显示当前 snapshot。
- Core 不可用时显示最后成功 snapshot + stale。
- 手动刷新不会产生并行 scan。
- “打开仪表盘”打开 loopback URL。
- LaunchAgent install/status/uninstall 幂等。
- 菜单栏退出不误删数据。

### 24.2 Claude parser

- 基础 assistant usage。
- message ID fork dedup。
- streaming zero → final usage largest-wins。
- cache creation 单独统计。
- 原生 reasoning 不重复估算。
- thinking carve。
- 非 Anthropic 不 carve。
- subagent token 计入。
- subagent 增长使自己的 checkpoint 失效。
- 不把 attachment 中“可用 skills 列表”当成统计数据。

### 24.3 Claude quota

- 5h/7d statusline 解析。
- 无 rate limit 不覆盖旧快照。
- 非法 stdin 不破坏旧快照。
- snapshot 原子写入。
- stale 判定。
- install 保留已有 statusLine。
- uninstall 恢复原值。
- 用户修改后的冲突不被覆盖。

### 24.4 第二期 Definition of Done

- macOS 登录后 Core 与菜单栏可自动启动。
- 菜单栏展示今日 token 和 Codex/Claude quota。
- Claude Code 历史 token 可在 Web 中查询。
- Codex 与 Claude 使用统一五类 token DTO。
- Claude fork 和 streaming 不重复。
- Claude quota 不读取 credential。
- 任一 provider 失败不影响另一 provider。
- Web 与菜单栏的 snapshot 数值一致。

## 25. 日志与隐私

### 25.1 可以记录

- provider ID。
- scan run ID。
- 文件数量、事件数量、耗时。
- 脱敏后的文件 basename。
- parser version。
- HTTP status category。
- quota source state。

### 25.2 禁止记录

- prompt、response、thinking 内容。
- JSONL 原始行。
- access token、refresh token、ID token。
- quota HTTP response body。
- 完整用户邮箱。
- 完整 cwd。
- Claude tool input。

### 25.3 数据导出

如提供 CSV/JSON 导出，只导出：

- 时间。
- source。
- model。
- project basename。
- 五类 token。
- total。
- quota 百分比和重置时间。

不提供 transcript 导出。

## 26. 推荐实施顺序

### 第一期

1. 初始化 Go、Vite 和 SQLite。
2. 建立 domain model、migration 和 repository。
3. 编写 Codex fixtures。
4. 实现 Codex discovery 和基础 parser。
5. 实现 token normalization 与 total 保真。
6. 实现 fork/replay/subagent dedup。
7. 实现 staging full rebuild。
8. 实现 summary、timeline、breakdown。
9. 实现 Codex quota adapter。
10. 实现 loopback API。
11. 实现 Web Dashboard 和 Diagnostics。
12. 实现增量 checkpoint。
13. 完成第一期验收测试。

### 第二期

1. 固化 `/api/v1/snapshot`。
2. 创建 SwiftUI MenuBarExtra。
3. 实现 LaunchAgent 管理。
4. 编写 Claude fixtures。
5. 实现 Claude main/subagent parser。
6. 实现 stable ID、fork、streaming dedup。
7. 实现 native reasoning 与 thinking carve。
8. 实现 Claude statusline inbox。
9. 实现 hook install/status/uninstall。
10. 在 Web 和菜单栏接入 Claude。
11. 完成第二期验收测试。

## 27. 最终边界

本文只交付以下两期：

- 第一期：本地 Web 仪表盘 + SQLite + Codex usage + Codex quota。
- 第二期：macOS 菜单栏 + Claude Code usage + Claude Code quota。

不同 Agent 之间的 session 浏览、恢复、迁移、启动和上下文管理不在本文设计或验收范围内。
