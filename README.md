# Dora

Dora 是一个运行在 macOS 本地的 AI 编程用量管理工具。当前使用 React 前端、Go 后端和 SQLite，并可采集本机真实 Codex token 用量。

## 构建环境

- Go 1.26.5
- Node.js 22.12+
- npm
- Make

本地开发和 CI 固定使用 Go 1.26.5；`backend/go.mod` 中的 `go 1.21` 仅表示源码和依赖的最低兼容版本。

## 安装依赖

```bash
make install
```

## 开发模式

```bash
make dev
```

该命令会同时启动：

- 后端：`http://127.0.0.1:8080`
- 前端：`http://127.0.0.1:5173`

浏览器访问：

```bash
open http://127.0.0.1:5173
```

页面会通过 Vite 的同源代理调用真实后端 API。“概览”展示本机真实 Codex token 总量、五类非重叠 token、API 等价美元估算、Cache 命中率、每日趋势、模型分布、项目分布和 53 周 Token 热力图；“诊断”展示扫描状态、文件数、存储事件数、parser 版本和初始化时间。

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

## 生产模式

构建当前 Mac 可直接运行的单进程程序：

```bash
make build
./bin/dora menubar
```

浏览器访问：

```bash
open http://127.0.0.1:8080
```

`make build` 会先清理旧前端产物，执行 Vite 生产构建，再把生成的页面资源嵌入 `bin/dora`。`menubar` 由同一个进程运行菜单栏、HTTP/API、SQLite、扫描器和配额服务，不会额外启动 `dora serve`、Node.js、npm 或 Vite。

构建会把 Git commit、工作区状态和 UTC 构建时间写入二进制。Dora 是个人本地工具，不维护独立版本号，也不要求为提交创建 Git tag；每个 Git commit 就是唯一构建来源。

```bash
./bin/dora status
```

`status` 输出短 commit 构建标识、完整 commit、`dirty/clean`、构建时间、Go 版本、`GOOS/GOARCH`、macOS 版本和构建来源。LaunchAgent 正常时这些字段来自运行中的服务，避免与尚未安装的新构建混淆。`serve` 与 `menubar` 每次启动都会把相同信息写入日志，但不会记录用户名、设备序列号、OAuth token 或其他凭证。

菜单栏标题显示今日 token；点开后可查看今日、7 日、全部 token、最高用量模型、Codex 5 小时/7 日配额和最近状态。菜单支持异步刷新、打开真实 loopback 仪表盘和正常退出。退出会关闭 HTTP 服务并释放端口。

不需要菜单栏时，也可以继续手动启动同一套运行时：

```bash
./bin/dora serve
```

生产程序仍只监听 `127.0.0.1`，并支持手动指定本地地址、数据库和 Codex 数据目录：

```bash
./bin/dora menubar \
  --addr 127.0.0.1:8080 \
  --db "$HOME/Library/Application Support/Dora/dora.db" \
  --codex-home "$HOME/.codex"
```

`--addr` 必须是明确的 `127.0.0.1:<port>`。端口被占用时 Dora 会直接报告冲突，不会终止旧进程或偷偷换端口。菜单中的“打开仪表盘”始终使用进程实际监听的地址。

### 登录后自动运行

从生产构建安装当前用户的 LaunchAgent：

```bash
./bin/dora install
```

该命令会把自包含二进制原子复制到：

```text
~/Library/Application Support/Dora/bin/dora
```

并安装 `~/Library/LaunchAgents/io.github.wubh576.dora.plist`。LaunchAgent 在当前用户进入 macOS 桌面后运行同一个 `dora menubar` 进程；无需 `sudo`，不会创建 LaunchDaemon 或第二个后端。

检查安装、加载、运行和真实 health API：

```bash
./bin/dora status
```

`status` 的退出码为：`0` 表示已安装且正常运行，`1` 表示未安装、未运行或运行异常，`2` 表示状态检查本身失败。输出的仪表盘地址固定为 `http://127.0.0.1:8080`。

在菜单中点击“退出 Dora”后，LaunchAgent 会把这次成功退出视为用户主动停止，因此本次登录会话不会立即拉起 Dora；下次登录 macOS 时仍会按 `RunAtLoad` 自动启动。需要立刻重新启动时，再执行一次 `./bin/dora install`。

卸载登录启动项和稳定位置中的二进制：

```bash
./bin/dora uninstall
```

卸载会停止 LaunchAgent，并删除 plist、安装二进制和安装临时文件；`dora.db`、`settings.json`、Codex 原始数据和日志都会保留。三个命令均只管理当前 macOS 用户，重复安装或卸载是安全的。

`go run ./cmd/dora install` 等开发构建不包含生产 Web 资源，会被拒绝。请始终先执行 `make build`，再运行 `./bin/dora install`。

### 日志与排障

LaunchAgent 的日志位于：

```text
~/Library/Logs/Dora/dora.stdout.log
~/Library/Logs/Dora/dora.stderr.log
```

后台 Codex 用量扫描和配额刷新失败时，日志会在单行内记录操作、底层错误原因以及重试建议或影响范围。错误不会包含 Authorization Header、OAuth token、control token、Cookie 或认证文件内容；Dora 主动退出产生的 context cancellation 不会记作后台失败。

LaunchAgent 启动时检查一次日志大小，之后每 10 分钟检查一次。`dora.stdout.log` 和 `dora.stderr.log` 分别以 `200 MiB（200 * 1024 * 1024 bytes）` 为活动文件阈值，不是两个文件合计 200 MiB。达到阈值后，Dora 将当前内容保存到同名 `.1` 文件并清空原活动文件；下一次轮转覆盖旧 `.1`，不会生成 `.2` 或无限历史。备份后采用 truncate 原文件，因此 launchd 已打开的文件描述符会继续写入原活动路径。

每个日志只保留一个备份，磁盘占用应合计评估 stdout/stderr 的两个活动文件和两个 `.1` 文件。轮转只在官方 launchd service、安装后二进制路径以及 stdout/stderr 文件描述符都与 Dora plist 匹配时启用；手动运行 `dora serve`、普通 `dora menubar`，甚至只手动追加 `--launchagent`，都不会操作这些日志。`dora uninstall` 仍会保留活动日志和备份。

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

配额读取默认开启。Dora 启动后会读取本地 Codex OAuth 登录并访问 ChatGPT 官方配额接口；可以随时在“诊断”中关闭“允许 Dora 读取订阅配额”。页面会展示 5 小时和 7 日窗口的已用比例、剩余比例、重置时间、过期状态和脱敏账号标签。

安全边界：

- 只接受 `auth.json` 中的 ChatGPT OAuth subscription；仅有 API key 时显示不支持。
- access token 只在单次请求的函数局部内存中使用，不写入 SQLite、`settings.json`、日志或 API。
- 只向代码中固定的 `chatgpt.com` 配额地址发送 OAuth header，不发送 prompt、token usage 或本地文件信息。
- 配额请求优先使用标准 `HTTPS_PROXY`/`NO_PROXY` 环境变量；未配置时自动继承 macOS 当前固定 HTTPS 或 SOCKS 系统代理。
- Clash Verge、ClashX、Shadowrocket 等工具只要启用了 macOS 系统代理或 TUN/VPN 路由即可生效；Dora 不识别应用名称，也不写死代理端口。
- 网络或登录失败保留最后一次成功配额，超过 10 分钟标记 stale；本地 token 统计不受影响。

默认开启时也可以从命令行手动刷新；如果已经在“诊断”中关闭，CLI 会遵循同一设置，不会绕过用户选择：

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

页面中的 token 总量使用英文紧凑数量级：`K`、`M`、`B`、`T`；需要核对时仍同时保留带千位分隔符的精确值。

费用使用 `backend/internal/pricing/catalog.json` 中的版本化定价目录计算。目录记录 OpenAI 官方来源和核对日期，当前核对于 `2026-07-31`；更新目录后只需重新构建 Dora，不需要重扫 Codex 会话或改写 SQLite。

费用是按照公开的标准 API 文本 token 价格得出的等价估算，不是 Codex 订阅的实际账单。Reasoning 按 output 价格计算；未匹配的模型和只有总量、缺少 token 分类的记录保持未定价，页面同时展示覆盖率。当前聚合数据无法可靠还原单次请求是否触发长上下文、区域处理、优先处理或工具调用附加费，因此这些费用不计入估算。

```text
GET /api/v1/summary?range=7D
GET /api/v1/timeline?range=30D&granularity=day
GET /api/v1/breakdown?range=30D&dimension=model
GET /api/v1/breakdown?range=30D&dimension=project
GET /api/v1/dashboard?range=7D
GET /api/v1/snapshot
```

`/api/v1/dashboard` 是 Web 页面使用的统一快照，保证标题总量、每日趋势和两个分布复用同一个时间窗口。`/api/v1/snapshot` 提供今日、7 日、全部 token、最高用量模型和扫描新鲜度，供后续本地客户端复用。

定价更新流程：

1. 在 OpenAI 官方模型页核对 input、cached input、cache write 和 output 价格。
2. 更新 `backend/internal/pricing/catalog.json` 的模型条目、来源和 `checkedAt`。
3. 运行 `make verify`；定价单元测试会验证目录、模型匹配和费用口径。

Dora 不在运行时抓取官网 HTML。若以后需要自动更新，应使用带版本和签名的定价清单，下载后完整校验再原子替换本地目录；在 OpenAI 提供稳定的机器可读定价接口前，不把网页结构当作运行时 API。

## 验证

```bash
make verify
```

提交前运行该命令，它会执行后端测试、清理并构建前端生产资源，以及验证带嵌入资源的 Go 生产程序可以构建。

推送到 `main` 或创建 Pull Request 后，GitHub Actions 会在 macOS 上使用 Go 1.26.5 和 Node.js 22 重新安装依赖、检查 Go module 是否干净，并执行相同验证与 `go vet`。
