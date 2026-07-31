# Dora：个人 AI Coding 用量 Demo 开发指南

> 状态：第一期、第二期 A 与第二期 B 已完成
> 目标平台：macOS，本地单用户
> 范围：第一期 Codex 本地 Web 仪表盘；第二期 A macOS 菜单栏；第二期 B Claude Code 本地用量
> 不在本文范围：多 Agent session 管理、团队协作、排行榜、云端同步

## 1. 最终目标

这个 Demo 是一个完全本地运行的个人 AI Coding 用量仪表盘：

- 第一期以 Codex 为唯一 usage provider；第二期 B 增加 Claude Code，并保持两者扫描与存储失败隔离。
- 实际 token 用量与订阅配额同等重要，但两条链路相互独立。
- 数据只存放在本机 SQLite，不需要 MySQL、PostgreSQL、Docker、登录系统或云服务。
- Web 页面只监听 `127.0.0.1`，由同一个 Go 核心进程提供 API 和静态资源。
- 第二期 A 增加原生 macOS 菜单栏；第二期 B 接入 Claude Code 的实际用量。
- 不保存 prompt、回复正文、工具参数或完整 transcript 副本。
- 不持久化 session ID、父子关系或完整项目路径；session 元数据只在扫描期间用于去重和聚合计数。

推荐的两期产品形态如下：

| 阶段 | 产品形态 | Usage 数据 | Quota 数据 |
|---|---|---|---|
| 第一期 | 本地 Web 仪表盘 | Codex transcript | Codex OAuth quota |
| 第二期 A | Web + macOS 菜单栏 | Codex transcript | Codex OAuth quota |
| 第二期 B | Web + macOS 菜单栏 | Codex + Claude Code transcript | Codex OAuth quota |

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
  → React 页面 / AppKit 菜单栏
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

第二期在 Go 生产程序内使用轻量 AppKit 状态栏桥接：

- `dora menubar` 是唯一进程，同时持有菜单栏、HTTP/API、SQLite、扫描器和配额服务。
- 菜单栏通过同一进程的 loopback API 读取规范化 snapshot，不直接查询 SQLite，也不复制统计逻辑。
- 复用 `dora serve` 的应用运行时，启动失败、信号退出和数据库关闭采用同一条路径。
- 状态项使用内嵌的单色 template icon，并设置为 accessory application，不创建窗口或 Dock 图标。
- 通过 LaunchAgent 运行同一个 `dora menubar` 进程，不额外启动 Core、Vite 或 Node 服务。

这一结构保留了 UI 与数据职责边界，同时避免两个常驻进程、端口协调和双重生命周期。

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
- 内部错误必须包含 provider 和当前操作；文件问题只保留脱敏后的必要原因，日志与 API 不记录 transcript 完整路径或 session ID。
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

```

### 7.4 为什么保存事件而不是只保存 bucket

30 分钟 bucket 适合多人和大规模聚合。个人版保存去重后的事件更合适：

- parser 修正后可以重建并核对单条事件。
- 定价目录变化时可以重新计算费用。
- 可以检查 reported total 与明细差异。
- 仍可通过 SQL 或 Go 聚合出 30 分钟和日级趋势。

不要保存原始 JSON 行。`source_files` 只保存增量扫描必需的 transcript 文件 checkpoint，`usage_events` 只保存统计元数据。每个 provider 独立完成 generation 切换；一个 provider 失败不得清空另一个 provider 的最后成功数据。

不建立 session 数据表。Codex 和 Claude Code 的 session ID、父子关系与项目完整路径只允许在 parser/去重过程的内存中短暂存在，不得进入 SQLite、日志或 API。Diagnostics 只暴露聚合后的 session 数。

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

- “读取 Codex 订阅配额”默认开启，设置页必须明确展示当前状态，并允许用户随时关闭；用户关闭后必须持久化其选择。
- access token 只保存在函数局部内存。
- 不写 SQLite、不写 settings、不写日志。
- URL hard-code 到允许的官方域名，不能被环境变量改写。
- 配额请求优先遵循标准 `HTTPS_PROXY`/`NO_PROXY` 环境变量；未配置时读取 macOS 当前固定 HTTPS 或 SOCKS 系统代理，代理只改变传输路径，不改变固定配额地址。
- Clash Verge、ClashX、Shadowrocket 等工具通过同一套 macOS 系统代理或 TUN/VPN 网络能力接入；实现不得识别代理应用名称、进程或写死本地端口。
- account ID 非空时，对固定配额地址同时发送 `ChatGPT-Account-ID`、`X-Account-ID` 和 `ChatClaude-Account-ID`，三者使用同一个值。
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

## 14. 费用估算

第一期展示 API 等价费用，但它仍是 token 的派生值：

```text
input / 1M × input price
+ output / 1M × output price
+ cache read / 1M × cache read price
+ cache creation / 1M × cache creation price
+ reasoning / 1M × reasoning price
```

要求：

- `backend/internal/pricing/catalog.json` 是第一期唯一的定价来源，随程序嵌入并纳入版本控制。
- 定价目录带版本、货币、官方来源和核对日期；更新目录后重新构建即可，不需要 migration。
- 先匹配精确模型 ID 和显式 alias，再按最长、最具体的 snapshot prefix 匹配。
- 未匹配模型显示“未定价”。
- 不使用一个任意默认价格静默估算未知模型。
- 只有总 token、缺少分类明细的记录不能猜测输入输出比例，保持未定价。
- reasoning token 使用对应模型的 output token 价格。
- GPT-5.6 cache write 使用官方公布的 uncached input `1.25x`；更早模型按目录中明确记录的标准 uncached input 价格计算。
- API 返回已定价 token、未定价 token、覆盖率、分类费用、官方来源和核对日期。
- 页面必须明确说明它是标准 API 等价估算，不是 Codex 订阅实际账单。
- 聚合事件无法可靠还原单次请求的上下文长度，不计算长上下文、区域处理、优先处理和工具调用附加费。
- 更新定价不需要重扫 transcript。

运行时不抓取 OpenAI 网页。官方模型页是面向人的文档，网页结构变化不应影响 Dora 启动和本地统计。未来如需自动更新，只接受带版本和签名的机器可读清单，经过 schema、来源、价格范围和签名验证后原子替换；在此之前由代码更新同步维护内置目录。

## 15. 本地 API

所有 API 使用 `/api/v1`：

| Method | Path | 用途 |
|---|---|---|
| GET | `/health` | 进程、数据库和 schema 状态 |
| GET | `/summary?range=7D` | 总 token、分项、cache rate、可选费用 |
| GET | `/timeline?range=30D&granularity=day` | 趋势 |
| GET | `/breakdown?range=30D&dimension=model` | 模型分布 |
| GET | `/breakdown?range=30D&dimension=project` | 项目分布 |
| GET | `/breakdown?range=30D&dimension=provider` | Provider 分布 |
| GET | `/breakdown?range=30D&dimension=provider_model` | 保留 Provider 归属的模型分布 |
| GET | `/quotas` | 最新 quota 与 stale 状态 |
| GET | `/snapshot` | 菜单栏使用的紧凑快照，与 Web dashboard 保持相同统计口径 |
| GET | `/diagnostics` | 数据源、扫描状态、错误 |
| POST | `/scan` | 手动触发扫描 |
| POST | `/quota/refresh` | 手动刷新 quota |
| GET/PUT | `/settings` | 本地设置 |

安全要求：

- 只监听 `127.0.0.1`，不监听 `0.0.0.0`。
- 前后端同源，不开放 CORS。
- 写接口验证 `Origin`。
- 启动时生成随机 control token，写接口要求 header。
- API 不返回 session、session ID、完整项目路径、`source_files.path`、access token 或原始错误 body。
- JSON 错误包含 provider、操作和可行动建议。

## 16. Web 仪表盘

### 16.1 页面

第一期只需要两个页面：

#### Dashboard

- 1D / 7D / 30D / ALL。
- 总 token；紧凑显示统一使用英文 `K`、`M`、`B`、`T` 数量级，并保留可核对的精确值。
- 普通输入、cache read、cache creation、普通输出、reasoning。
- cache 命中率：

```text
cache read / (input + cache read + cache creation)
```

- 每日趋势堆叠图。
- GitHub 风格的 53 周 Token 热力图，明确展示统计起始日；热力图通过统一 API 获取完整活动数据，不跟随当前汇总范围截断。
- 模型分布。
- 项目分布。
- Codex 与 Claude Code 各自的 token 总量、事件数和主要模型。
- 可选费用。
- Codex 5h/7d quota 卡片。

#### Diagnostics / Settings

- Codex 与 Claude Code 各自的配置发现、聚合 session 数、最近扫描时间。
- 各自的扫描文件数、事件数和 parser version。
- quota consent。
- quota 最后成功时间。
- 错误与修复建议。
- “全量重建”按钮。

### 16.2 空态与错误态

- 两个 provider 都没有目录：提示先运行 Codex 或 Claude Code。
- 只有 Claude Code 没有本地会话：明确展示“暂无 Claude Code 本地会话”，不影响 Codex 数据。
- 有目录但没有 token event：展示扫描文件数和 provider 诊断，不展示完整路径。
- quota 未启用：usage 正常显示，quota 卡片显示未启用。
- quota 请求失败：显示最后成功值和 stale badge。
- parser 部分失败：显示旧数据，不显示虚假的全零。

### 16.3 Snapshot DTO

菜单栏使用以下紧凑 DTO；Web 使用 `/dashboard`，两者复用同一 analytics 统计口径：

```json
{
  "generatedAt": "2026-07-30T12:00:00Z",
  "usage": {
    "todayTokens": 123456,
    "sevenDayTokens": 765432,
    "allTimeTokens": 9876543,
    "topModel": "gpt-5.6-sol",
    "lastScanAt": "2026-07-30T11:59:20Z",
    "stale": false,
    "providers": [
      {"source": "provider.codex", "tokens": 7000000},
      {"source": "provider.claude-code", "tokens": 2876543}
    ]
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
4. 生产构建由同一进程提供嵌入式 Web 静态资源，开发模式继续由 Vite 提供页面。
5. 异步启动一次 usage scan。
6. quota 已授权时异步刷新。
7. 每 5 分钟检查增量 usage 和 quota。

不要在 HTTP server ready 之前执行耗时全量扫描。

第一期由用户手动打开 loopback 页面，不自动打开浏览器；后续由菜单栏和 LaunchAgent 接管启动入口。

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

# 第二期：macOS 菜单栏与 Claude Code 本地用量

## 19. 第二期架构

```text
Codex / Claude files ──→ 单个 dora menubar 进程
                         ├─ Go runtime + SQLite
                         ├─ loopback HTTP/API + 内嵌 React
                         └─ AppKit status item
```

原则：

- SQLite 仍然只有 Dora runtime 一个写入者。
- 菜单栏不复制 analytics SQL。
- Web 使用 dashboard DTO，菜单栏使用 compact snapshot DTO；两者调用同一 analytics 层并保持统计口径一致。
- Provider 独立失败。

## 20. macOS 菜单栏

### 20.1 功能

菜单栏标题默认显示今日 token，例如：

```text
12.4M
```

菜单：

- 1D / 7D / ALL token。
- top model。
- Codex 5h/7d quota。
- 最后扫描和刷新时间。
- “立即刷新”。
- “打开仪表盘”。
- “退出”。

当前菜单使用统一 snapshot 展示 Codex + Claude Code 用量，并单独展示 Codex 5h/7d quota。菜单不实现复杂趋势图、项目表格或设置页面。

### 20.2 刷新

- 打开菜单时读取 snapshot，后台每分钟重读当前状态；扫描与配额后台周期仍由共享 runtime 统一管理。
- 手动刷新在后台 goroutine 中先扫描 token，再按用户授权刷新配额，不能并发触发，也不能阻塞菜单事件循环。
- 配额刷新失败不能回滚新的 token 数据；刷新结束后重新读取 snapshot。
- 菜单栏复用 loopback API DTO，不自行解析文件、查询 SQLite 或实现另一套统计。
- “打开仪表盘”用参数化系统命令打开 runtime 的实际 loopback 地址。
- 退出、SIGINT 和 SIGTERM 都取消同一个 application context，正常关闭 HTTP、后台任务和 SQLite。

### 20.3 LaunchAgent

第二期提供显式命令：

```text
dora install
dora status
dora uninstall
```

安装内容：

- 当前生产二进制原子复制到 `~/Library/Application Support/Dora/bin/dora`。
- plist 固定为 `~/Library/LaunchAgents/io.github.wubh576.dora.plist`。
- Label 固定为 `io.github.wubh576.dora`。
- stdout/stderr 分别写入 `~/Library/Logs/Dora/dora.stdout.log` 和 `dora.stderr.log`。
- `RunAtLoad=true`
- `KeepAlive.SuccessfulExit=false`
- `ThrottleInterval=10`

要求：

- 只使用当前用户的 `gui/<uid>` domain，不使用 sudo、system domain、LaunchDaemon 或废弃的 load/unload。
- install 只接受包含生产 Web 资源的可执行文件；开发构建提示先运行 `make build`。
- plist 使用安装后二进制的绝对路径，ProgramArguments 为该路径、`menubar` 和用于启用受管日志轮转的 `--launchagent` 标记。
- 二进制与 plist 都先写明确临时文件，再 rename 替换；重装按 `print → bootout → bootstrap → kickstart` 安全重载。
- install 等待 loopback health，重复执行不会产生多个 LaunchAgent 或 Dora 常驻进程。
- status 综合 plist、安装二进制、`launchctl print` 和 health；退出码 0 为正常，1 为未安装/未运行/异常，2 为检查失败。
- uninstall 幂等，只删除 plist、安装二进制和对应临时文件，保留 SQLite、settings、日志和 Codex 原始数据。
- 菜单“退出 Dora”产生成功退出，当前登录会话不立即重启；下次登录仍由 RunAtLoad 启动。
- stdout 和 stderr 各自以 200 MiB 为活动文件阈值；启动时检查一次，运行中每 10 分钟检查一次，任一侧失败不阻断另一侧或应用主流程。
- 达到阈值时先原子覆盖同名 `.1` 备份，再 truncate 原活动文件，保证 launchd 已打开的文件描述符继续写入原 inode；每侧只保留一个备份，不生成 `.2`。
- 轮转由共享 application runtime 管理 context 生命周期。启用前必须同时确认 `XPC_SERVICE_NAME` 为官方 Label、当前可执行文件位于安装路径，并且 stdout/stderr 文件描述符与 plist 的两个日志为同一文件；手动 `serve`、普通 `menubar` 或只伪加 `--launchagent` 都不得操作已安装日志。

### 20.4 构建来源与启动环境

- Dora 是个人本地工具，不维护独立版本号，也不要求为每个提交创建 Git tag；Git commit 是唯一构建来源。
- `make build` 通过 Go `-ldflags` 注入完整 Git commit、工作区 `dirty/clean` 状态和 UTC build time。
- 构建标识使用 12 位短 commit，脏工作区追加 `-dirty`；该标识只用于定位源码，不作为产品版本号。
- 不提供独立 `dora version` 命令。`dora status` 输出构建标识、完整 commit、工作区状态、构建时间、Go version、`GOOS/GOARCH`、macOS product version 和构建来源。
- LaunchAgent 正常时，`status` 通过本机 health API 读取运行中实例的 build info，不得混用调用命令的信息。
- `serve` 和 `menubar` 启动日志记录同一份 build info；日志不得包含用户名、设备序列号、OAuth token 或其他凭证。

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

缺失目录不是错误。`message.model` 始终保留 transcript 原始值，因此 Claude Code 使用 Anthropic-compatible endpoint 的其他模型时仍可统计；Dora 不识别或读取第三方 endpoint 凭证。

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

缺少 message ID 时跳过该 usage 并报告 degraded。record UUID 可能随 streaming flush 变化，不能作为 logical message identity；同时禁止使用 prompt、response 或 thinking 正文生成内容指纹。

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
- session ID、父子关系和完整项目路径只在扫描期间临时使用，不写入 SQLite、日志或 API。

SQLite 只保存 provider-scoped usage generation、文件 checkpoint 和聚合后的配置发现状态、主 session 数、parser 版本。Codex 与 Claude Code 分别原子提交；单个 provider 失败保留它自己的上一次成功数据，不回滚另一个 provider。

## 23. Claude Code 配额（后续范围）

当前不实现 Claude Code 配额。Web 和菜单栏中的 5 小时/7 日配额仍明确属于 Codex；以下仅保留为后续设计草案。

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
- 菜单栏展示 Codex + Claude Code 合并后的今日 token，并明确标注 Codex quota。
- Claude Code 历史 token 可在 Web 中查询。
- Codex 与 Claude 使用统一五类 token DTO。
- Claude fork 和 streaming 不重复。
- Claude Code quota 不在当前范围；现有 quota 始终属于 Codex。
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
- 后台扫描或配额刷新失败的操作上下文、底层错误原因和可行动建议。

后台失败日志必须保持单行。正常 context cancellation 或 Dora 主动退出不记录为失败；配额网络和认证错误在 provider 边界保留有价值的 error chain，但不得包含请求 Header、OAuth token、Cookie 或响应正文。

LaunchAgent 的 stdout 和 stderr 活动日志分别达到 200 MiB 时轮转，各覆盖一个 `.1` 备份。阈值按单个活动文件计算，不按两个日志合计；磁盘占用评估必须同时计入两个活动文件和两个备份。轮转失败只记录单行原因并在下个周期重试，不影响 Web、菜单栏、扫描或配额服务；uninstall 不删除活动日志或备份。

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
2. 实现单进程 AppKit 菜单栏状态项。
3. 实现 LaunchAgent 管理。
4. 编写 Claude fixtures。
5. 实现 Claude main/subagent parser。
6. 实现 stable ID、fork、streaming dedup。
7. 实现 native reasoning 与 thinking carve。
8. 在 Web 和菜单栏接入 Claude，并保留 provider 归属。
9. 完成第二期验收测试。

## 27. 最终边界

本文只交付以下两期：

- 第一期：本地 Web 仪表盘 + SQLite + Codex usage + Codex quota。
- 第二期：macOS 菜单栏 + Claude Code usage；订阅 quota 仍只支持 Codex。

不同 Agent 之间的 session 浏览、恢复、迁移、启动和上下文管理不在本文设计或验收范围内。
