# Dora

Dora 是一个运行在 macOS 本地的 AI 编程用量管理工具。当前使用 React 前端、Go 后端和 SQLite，并可只读采集本机真实 Codex 与 Claude Code token 用量。

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

页面会通过 Vite 的同源代理调用真实后端 API。“概览”展示本机真实 Codex + Claude Code token 总量、两个 provider 各自的 token 与模型、五类非重叠 token、API 等价美元估算、Cache 命中率、每日趋势、模型分布、项目分布和 53 周 Token 热力图；“诊断”分别展示两个 provider 的配置发现、会话计数、扫描文件、存储事件和 parser 版本。

概览支持 `1D`、`7D`、`30D` 和 `ALL`。Dora 的用量统计统一始于 `2026-07-01`，所有范围按 macOS 本地时区的日历日计算；汇总、趋势、分布和热力图来自同一份 SQLite 数据快照。所选范围可以改变汇总与趋势，热力图始终保留从统计起始日至今的完整足迹。

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

菜单栏标题通常显示今日 token；Codex 有等待授权、危险命令确认或用户问题时切换为 `🔴 N`。顶部“需要关注”子菜单会按 waiting session 展示 Codex App/iTerm2/Terminal、项目 basename、等待原因、时长和请求数，点击可精确回到对应 thread 或 TTY；点击不会把请求标记为已处理。菜单继续展示今日、7 日、全部 token、Codex 5 小时/7 日配额和最近状态，并支持异步刷新、打开真实 loopback 仪表盘和正常退出。退出会关闭 HTTP 服务并释放端口。

不需要菜单栏时，也可以继续手动启动同一套运行时：

```bash
./bin/dora serve
```

生产程序仍只监听 `127.0.0.1`，并支持手动指定本地地址、数据库、Codex 数据目录和 Claude Code 配置目录：

```bash
./bin/dora menubar \
  --addr 127.0.0.1:8080 \
  --db "$HOME/Library/Application Support/Dora/dora.db" \
  --codex-home "$HOME/.codex" \
  --claude-home "$HOME/.claude"
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

### Codex 实时提醒

生产二进制安装到稳定路径后，为 Codex 安装结构化生命周期 hooks：

```bash
"$HOME/Library/Application Support/Dora/bin/dora" hooks install codex
"$HOME/Library/Application Support/Dora/bin/dora" hooks status codex
```

安装会原子合并 `~/.codex/hooks.json`，保留其他工具和用户已有的 hooks；重复执行会更新 Dora 可执行文件路径，不产生重复 handler。Codex 自己的 hook 信任机制不会被绕过：首次安装或 Dora 路径变化后，在 Codex 中打开 `/hooks`，检查命令与配置来源后明确授权。状态命令会报告配置路径、Dora 路径、缺失/损坏事件和信任状态。

卸载只移除带 Dora 标记的 handler：

```bash
"$HOME/Library/Application Support/Dora/bin/dora" hooks uninstall codex
```

Hook helper 只向固定的 `127.0.0.1:8080` 发送有界、脱敏 JSON，不跟随重定向。Dora 不保存 prompt、回复、完整命令、工具参数、环境变量、transcript 路径或完整 cwd；SQLite 只在实时 session 存活期间保存 external session ID、cwd basename、模型、App/CLI surface、受支持终端的精确 TTY 和等待状态。`SessionEnd` 或跳转确认目标已消失时会移除 runtime session；重启后保留尚未结束的 waiting 展示，但不会对历史请求重复发声。

PermissionRequest 不存在假想的即时 resolved 回调。Dora 在同 Session 后续出现 `PostToolUse`、`UserPromptSubmit`、`Stop` 或 `SessionEnd` 时解除 waiting；因此 Allow 后通常要等工具完成，Deny/Cancel 要等 Stop 或下一次结构化活动，菜单计数才会回落。若进程异常退出且缺失结束事件，超过 7 天没有任何 Hook 活动的 runtime session 会在启动和定时 reconciliation 中清理，避免永久残留，同时不会用几秒钟的盲目 timeout 提前解除真实 waiting。

当前跳转支持 Codex App deep link、iTerm2 exact TTY 和 macOS Terminal exact TTY。首次回跳终端时，macOS 可能询问是否允许 Dora 控制 iTerm2 或 Terminal；允许后才能选择准确 Tab。未知终端不会使用窗口标题或 cwd 猜测目标；这类 session 仍可显示提醒，但会明确提示不能精确跳转。

可靠主动提醒路径是菜单栏 `🔴 N` 和系统自带 `Glass` 声音。Dora 当前以 LaunchAgent 启动独立二进制，不是具有稳定 bundle identifier 的 `.app`；Apple 的通知中心 API 面向 app 或 app extension，因此当前不伪造不可靠的系统通知横幅。Claude Code 实时提醒和刘海 UI 不在本次范围内。

### 日志与排障

LaunchAgent 的日志位于：

```text
~/Library/Logs/Dora/dora.stdout.log
~/Library/Logs/Dora/dora.stderr.log
```

后台 provider 用量扫描和 Codex 配额刷新失败时，日志会在单行内记录操作、脱敏后的错误原因以及重试建议或影响范围。Codex 实时日志只使用 12 位不可逆 session 短哈希，记录 Hook 事件、状态结果、attention 创建/去重/解除、提醒批次和回跳结果，不记录原始 session ID。错误不会包含 transcript 完整路径、Authorization Header、OAuth token、control token、Cookie 或认证文件内容；Dora 主动退出产生的 context cancellation 不会记作后台失败。

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

Dora 的用量扫描只保存 token 统计元数据、脱敏项目名和扫描 checkpoint，不保存 usage session、session ID、父子关系、完整项目路径、prompt、回复正文、工具参数或 JSONL 原始行。Codex 原始文件和 Dora SQLite 数据库都不会提交到 Git。

## Claude Code 本地用量扫描

Dora 后端的自动扫描、`dora scan` 和菜单栏刷新都会同时只读检查：

```text
~/.claude/projects/<project>/<session>.jsonl
~/.claude/projects/<project>/<session>/subagents/agent-*.jsonl
```

配置目录优先使用显式 `--claude-home`，其次使用 `CLAUDE_CONFIG_DIR`，最后使用 `~/.claude`。目录不存在是正常的“暂无数据”，不会影响 Codex。Claude Code 可以连接 Anthropic-compatible endpoint 并使用其他模型；Dora 始终保存 transcript 中 `message.model` 的原始值，不把 Claude Code 等同于 Anthropic 模型，也不会为未匹配定价目录的模型编造价格。

只统计 assistant `message.usage` 中的 input、output、cache read、cache creation 和原生 reasoning；Anthropic usage 中的 5 分钟与 1 小时 cache creation 会分开保存并按不同价格计算。稳定 `message.id` 用于跨 streaming flush、fork 和 subagent 去重；缺少 message ID 时跳过该 usage 并把 diagnostics 标记为 degraded，避免不稳定 record UUID 导致重复统计。主 session 与 subagent 只在扫描期间临时关联，Dora 不建立 session 表，不保存 session ID、父子关系、完整项目路径或 transcript 内容。

手动指定隔离目录扫描：

```bash
cd backend
go run ./cmd/dora scan \
  --codex-home /path/to/codex-home \
  --claude-home /path/to/claude-config
```

每个 provider 独立提交 SQLite generation。Claude Code 文件损坏或权限不足时保留上一次成功的 Claude 统计，同时 Codex 扫描仍可成功；反向同理。Claude Code 配额不在当前范围内，页面和菜单栏中的订阅配额仍只属于 Codex。

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

费用使用 `backend/internal/pricing/catalog.json` 中的版本化定价目录计算。目录记录逐模型价格、对应厂商官方来源和核对日期，当前核对于 `2026-08-01`；单纯更新价格后只需重新构建 Dora，不需要重扫 transcript 或改写 SQLite。

价格只按 transcript 中的真实模型 ID 匹配，不与 Codex、Claude Code 等 Agent 框架绑定。Claude Code 调用 GPT 时按 GPT 价格，Codex 调用 Claude 时按 Claude 价格；Kimi 等第三方模型只有存在自己的显式官方定价条目时才计算，否则保持未定价，绝不会套用当前 Agent 的默认价格。

当前目录支持 Kimi K3 的官方模型 ID `kimi-k3`：cache miss 输入与 cache creation 按 `$3.00 / 1M tokens`、cache hit 按 `$0.30 / 1M tokens`、输出和 reasoning 按 `$15.00 / 1M tokens` 估算。Claude Code 环境变量中的 `kimi-k3[1m]` 只是上下文选择写法，Dora 仍以 transcript 返回的 `kimi-k3` 为准，不把环境配置字符串登记为模型 alias。

模型 ID 匹配保持保守：Claude 4.6 及以后只接受官方无日期固定 ID，4.5 及更早只接受目录中明确登记的官方 alias 或完整日期 ID；带 `custom`、`preview` 等第三方后缀的兼容模型不会自动套用 Anthropic 价格。

费用是按照公开的标准 API 文本 token 价格得出的等价估算，不是 Codex、Claude 或 Kimi 订阅的实际账单。Reasoning 按对应模型的 output 价格计算；Anthropic 5 分钟和 1 小时 cache write 使用各自价格，缺少时长明细的 Claude cache write 保持未定价。未匹配模型、只有总量的记录和无法分类的 token 同样保持未定价，页面同时展示覆盖率。当前聚合数据无法可靠还原单次请求是否触发长上下文、区域处理、优先处理、Fast mode 或工具调用附加费，因此这些费用不计入估算。

```text
GET /api/v1/summary?range=7D
GET /api/v1/timeline?range=30D&granularity=day
GET /api/v1/breakdown?range=30D&dimension=model
GET /api/v1/breakdown?range=30D&dimension=project
GET /api/v1/breakdown?range=30D&dimension=provider
GET /api/v1/breakdown?range=30D&dimension=provider_model
GET /api/v1/dashboard?range=7D
GET /api/v1/snapshot
GET /api/v1/attention
```

这些 API 的总量默认合并 Codex 与 Claude Code；`providers` 和 `provider_model` 保留来源归属，即使两个 provider 报告同名模型也能区分。`/api/v1/dashboard` 是 Web 页面使用的统一快照，保证标题总量、每日趋势和分布复用同一个时间窗口。`/api/v1/snapshot` 提供合并后的今日、7 日、全部 token、最高用量模型、provider 总量和扫描新鲜度，供菜单栏复用；其中订阅配额仍明确只属于 Codex。

定价更新流程：

1. 在模型对应厂商的官方页面核对 input、cached input、cache write 和 output 价格。
2. 更新 `backend/internal/pricing/catalog.json` 的精确模型条目、HTTPS 厂商官方来源和 `checkedAt`；不要按 Agent 框架或过宽模型家族设置默认价格。未公布的 cache 价格可以留空，相应 token 会保持未定价。
3. 运行 `make verify`；定价单元测试会验证目录、模型匹配和费用口径。

Dora 不在运行时抓取厂商官网 HTML。若以后需要自动更新，应使用带版本和签名的定价清单，下载后完整校验再原子替换本地目录；在厂商提供稳定的机器可读定价接口前，不把网页结构当作运行时 API。

## 验证

```bash
make verify
```

提交前运行该命令，它会执行后端测试、清理并构建前端生产资源，以及验证带嵌入资源的 Go 生产程序可以构建。

推送到 `main` 或创建 Pull Request 后，GitHub Actions 会在 macOS 上使用 Go 1.26.5 和 Node.js 22 重新安装依赖、检查 Go module 是否干净，并执行相同验证与 `go vet`。
