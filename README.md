# Dora

Dora 是一个运行在 macOS 本地的 AI 编程用量管理工具。当前使用 React 前端、Go 后端和 SQLite，并可采集本机真实 Codex token 用量。

## 环境要求

- Go 1.20+
- Node.js 20.19+ 或 22.12+
- npm
- Make

## 安装依赖

```bash
make install
```

## 本地启动

```bash
make dev
```

该命令会同时启动：

- 后端：`http://127.0.0.1:8080`
- 前端：`http://127.0.0.1:5173`

浏览器访问：

```text
http://127.0.0.1:5173
```

页面会通过 Vite 的同源代理调用真实后端 API。“概览”展示本机真实 Codex token 总量、五类非重叠 token、Cache 命中率、每日趋势、模型分布、项目分布和 53 周 Token 热力图；“诊断”展示扫描状态、文件数、存储事件数、parser 版本和初始化时间。

概览支持 `1D`、`7D`、`30D` 和 `ALL`。Dora 的用量统计统一始于 `2026-07-29`，所有范围按 macOS 本地时区的日历日计算；汇总、趋势、分布和热力图来自同一份 SQLite 数据快照。所选范围可以改变汇总与趋势，热力图始终保留从统计起始日至今的完整足迹。

状态 API：

```text
GET http://127.0.0.1:8080/api/v1/health
```

SQLite 默认保存在：

```text
~/Library/Application Support/Dora/dora.db
```

首次启动时后端会创建目录、数据库、migration 和 Dora 初始化记录。后续启动会读取同一条初始化记录，不会重置初始化时间。

## Codex 本地用量扫描

后端启动后会立即扫描，并每 5 分钟检查一次新增记录；每 24 小时至少执行一次全量校验：

```text
~/.codex/sessions/
~/.codex/archived_sessions/
```

支持递归读取 `.jsonl`、`.jsonl.gz` 和 `.jsonl.zst`。首次扫描执行 source 级原子重建；后续扫描跳过未变化文件，对安全的 JSONL 追加内容执行增量解析。解析失败不会替换上一次成功数据。

手动扫描：

```bash
make scan
```

强制全量校验：

```bash
cd backend
go run ./cmd/dora scan --full
```

使用 `CODEX_HOME` 覆盖默认目录，或重复传入 `--codex-home` 扫描多个目录：

```bash
cd backend
go run ./cmd/dora scan --codex-home /path/to/codex-home
```

扫描状态 API：

```text
GET http://127.0.0.1:8080/api/v1/diagnostics
```

页面使用的手动扫描接口为 `POST /api/v1/scan`。该写接口同时校验本次后端启动生成的 control token 和本地页面 `Origin`。

Dora 只保存 token 统计元数据、脱敏项目名和扫描 checkpoint，不保存 prompt、回复正文、工具参数或 JSONL 原始行。Codex 原始文件和 Dora SQLite 数据库都不会提交到 Git。

## Codex 订阅配额

配额读取默认关闭。在页面进入“诊断”，明确开启“允许 Dora 读取订阅配额”后，Dora 才会读取本地 Codex OAuth 登录并访问 ChatGPT 官方配额接口。页面会展示 5 小时和 7 日窗口的已用比例、剩余比例、重置时间、过期状态和脱敏账号标签。

安全边界：

- 只接受 `auth.json` 中的 ChatGPT OAuth subscription；仅有 API key 时显示不支持。
- access token 只在单次请求的函数局部内存中使用，不写入 SQLite、`settings.json`、日志或 API。
- 只向代码中固定的 `chatgpt.com` 配额地址发送 OAuth header，不发送 prompt、token usage 或本地文件信息。
- 网络或登录失败保留最后一次成功配额，超过 10 分钟标记 stale；本地 token 统计不受影响。

开启页面授权后，也可以从命令行手动刷新：

```bash
make quota
```

配额与设置 API：

```text
GET /api/v1/quotas
POST /api/v1/quota/refresh
GET /api/v1/settings
PUT /api/v1/settings
```

## Token 统计 API

```text
GET /api/v1/summary?range=7D
GET /api/v1/timeline?range=30D&granularity=day
GET /api/v1/breakdown?range=30D&dimension=model
GET /api/v1/breakdown?range=30D&dimension=project
GET /api/v1/dashboard?range=7D
GET /api/v1/snapshot
```

`/api/v1/dashboard` 是 Web 页面使用的统一快照，保证标题总量、每日趋势和两个分布复用同一个时间窗口。`/api/v1/snapshot` 提供今日、7 日、全部 token、最高用量模型和扫描新鲜度，供后续本地客户端复用。

## 验证

```bash
make verify
```

该命令会运行后端测试并构建前端。
