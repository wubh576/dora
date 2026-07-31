# Dora 实现状态

本文档记录 Dora 第一期各里程碑的实现状态、验证结果和对应提交。只有功能、测试、端到端自测、Code Review、提交与推送全部完成后，里程碑才标记为完成。

## 里程碑概览

| 里程碑 | 状态 | Commit |
| --- | --- | --- |
| 1. 基础运行链路 | 已完成 | `68db287` |
| 2. Codex 本地用量采集 | 已完成 | `738d1a9` |
| 3. Token 统计 API 与 Web 仪表盘 | 已完成 | `7718e42` |
| 4. Codex 订阅配额 | 已完成 | `accba4e` |
| 5. 第一期整体收尾 | 已完成 | `09d822e` |
| 6. 中文桌面界面与 Token 热力图 | 已完成 | `b5a4875` |
| 7. Token 数量级与默认配额 | 已完成 | `4afa3a0` |
| 8. 单进程生产运行 | 已完成 | `98ac8b2` |
| 9. macOS 系统代理适配 | 已完成 | `62218e3` |
| 10. Token API 等价费用估算 | 已完成 | `fa607f2` |
| 11. 单进程 macOS 菜单栏 | 已完成 | `66a8c48` |
| 12. 当前用户 LaunchAgent 生命周期 | 已完成 | `690e214` |
| 13. 构建来源与启动环境信息 | 已完成 | `0fae6ff` |
| 14. 后台失败日志真实原因 | 已完成 | `94a911c` |
| 15. LaunchAgent 日志自动轮转 | 已完成 | `10c9463` |
| 16. Provider 隔离与 Claude Code 只读采集 | 已完成 | `7a88a17`、`e2c905f` |
| 17. 多 Provider 统一展示 | 已完成 | `ed41bf8`、`a950d69` |
| 18. 统一统计起始日 | 已完成 | 本次提交 |

## 里程碑 1：基础运行链路

已完成：

- 建立 `backend/`、`frontend/` 和 `docs/` 目录边界。
- Go 后端仅监听 `127.0.0.1`，提供真实状态 API。
- SQLite 启动时自动创建数据库、执行 migration 并持久化 Dora 初始化时间。
- React 前端通过 Vite 同源代理调用后端 API，展示后端、SQLite 和初始化状态。
- README 提供依赖安装、开发启动和验证命令。
- `.gitignore` 排除数据库、日志、构建产物和凭证。

验证记录：

- `make verify`：后端全部测试通过，前端 TypeScript 检查与生产构建通过。
- 浏览器端到端验证：页面能够显示 `Dora is running`、`Backend connected`、`SQLite ready` 和真实初始化时间。
- 持久化验证：重启后端并刷新页面后，初始化时间保持不变。
- Code Review：已完成；修复数据库对既有父目录权限的意外修改，并明确 Vite 所需 Node.js 版本。
- Commit：`68db287 feat(app): initialize full-stack status flow`
- 推送分支：`main`

## 里程碑 2：Codex 本地用量采集

已完成：

- 自动发现 `~/.codex/sessions` 和 `~/.codex/archived_sessions`，支持 `CODEX_HOME` 与多个 `--codex-home` 覆盖。
- 解析 `.jsonl`、`.jsonl.gz` 和 `.jsonl.zst`，按真实 Codex 字段归一化 input、output、cached input、cache creation、reasoning 和 total。
- 优先累计 `last_token_usage`，使用 usage tuple 与 running total 生成不含时间、路径和 session ID 的稳定 dedup key。
- 实现 largest-wins、跨文件去重和 fork/subagent lineage-aware inherited replay 处理；缺失、冲突、循环或跨 home 的 lineage 会安全保留。
- migration 建立扫描、文件 checkpoint、usage staging、usage event 和 provider 状态表及索引。
- 首次执行 source 级原子重建；后续跳过未变化文件并安全解析 JSONL append；解析失败不替换旧 generation。
- 后端启动时立即扫描，此后每 5 分钟检查增量，并每 24 小时至少执行一次全量校验。
- 提供 `dora scan`、`POST /api/v1/scan` 和 `GET /api/v1/diagnostics`；写接口校验本次启动的 control token 与本地页面 Origin。
- SQLite 主文件、WAL 和 SHM 权限为 `0600`，读取连接与 staging 写入隔离。

验证记录：

- 当前机器真实数据：临时数据库首扫 42 个 Codex 文件，解析 2430 条 token 记录并去重为 968 条；紧接着第二次扫描解析 0 条新增，结果仍为 968 条。
- 增量实测：当前会话产生新记录后，只解析 append 内容并增加对应去重事件。
- API 端到端：health、diagnostics 和受保护的手动扫描均成功；API 未返回原始路径、原始错误或 transcript 内容。
- 持久化：复用同一临时数据库重启后，Dora 初始化时间与 usage generation 保持不变。
- `make verify`：后端测试通过，前端 TypeScript 检查和生产构建通过。
- `go test -race ./...`、`go vet ./...`、`git diff --check`：通过。
- Token fixture 覆盖 fork 完整复制、时间戳改写、inherited prefix、新 usage、父缺失、lineage 冲突与循环、跨 home、largest-wins、残行、truncate/replace、压缩变化、每日全量和失败保旧 generation。
- Code Review：独立 Reviewer 首轮发现 8 项问题，全部修复；复审通过，无剩余阻塞问题。
- Commit：`738d1a9 feat(usage): collect local Codex usage`
- 推送分支：`main`

## 里程碑 3：Token 统计 API 与 Web 仪表盘

已完成：

- 提供 `summary`、`timeline`、`breakdown`、`dashboard` 和 `snapshot` API。
- `Today`、`7D`、`30D` 和 `All` 按本地日历生成半开区间，汇总、每日趋势和分布复用同一份 SQLite 快照。
- 展示总 token、input、cache read、cache creation、output、reasoning、reported total 与 cache 命中率。
- Web Dashboard 展示真实每日堆叠趋势、模型分布、项目分布、空态和扫描错误态。
- Diagnostics 展示扫描状态、文件数、存储事件数、parser 版本，支持增量扫描与全量重建。
- 趋势图提供屏幕阅读器可访问的数据表，页面按钮和异步状态具有明确的无障碍语义。

验证记录：

- 当前机器真实数据：Dashboard 能展示 SQLite 中的真实 Codex token，`Today` 与 `7D` 切换后总量、趋势和分布同步更新。
- 浏览器端到端：Dashboard、Diagnostics、增量扫描、全量重建和扫描后状态刷新均通过；控制台无 JavaScript 错误。
- `make verify`、`go test -race ./...`、`go vet ./...`、`git diff --check`：通过。
- Analytics 测试覆盖本地午夜、半开区间、春季跳时、秋季回拨、summary 与 timeline 一致、cache rate、模型/项目分布和 SQLite 最新 generation 可见性。
- Code Review：独立 Reviewer 发现跨请求快照、snapshot 多次读取、DST 事件覆盖、错误态刷新和趋势无障碍问题；全部修复后复审通过。
- Commit：`7718e42 feat(dashboard): visualize Codex token usage`
- 推送分支：`main`

## 里程碑 4：Codex 订阅配额

已完成：

- 配额读取默认关闭，只有用户在 Diagnostics 明确授权后才读取本地 Codex OAuth 登录并请求固定的 ChatGPT 配额地址。
- 按窗口时长识别 5 小时和 7 日配额，不依赖 primary/secondary 顺序；仅主地址返回 404 时尝试备用地址。
- SQLite 保存最后成功配额，失败只更新 provider 状态；网络或登录失败保留旧值，超过 10 分钟标记 stale。
- 提供配额、设置和手动刷新 API，以及 `dora quota refresh` 命令；后台最多每 5 分钟自动刷新一次。
- Dashboard 展示已用、剩余、重置时间、账号脱敏标签和 stale 状态；Diagnostics 展示授权、状态、最后成功时间和手动刷新入口。
- access token 只用于单次请求，不写入 SQLite、settings、日志或 API；HTTP 禁止重定向，避免认证头离开固定地址。

验证记录：

- `make verify`、`go test -race ./...`、`go vet ./...`、`git diff --check`：通过。
- 浏览器端到端：默认未授权时 Dashboard 正常展示本地 usage，配额卡片显示未启用；Diagnostics 显示明确且未勾选的授权开关。
- 测试覆盖 5h/7d 窗口交换、404 fallback、401/403、百分比边界、singleflight、5 分钟缓存、stale fallback、失败保留历史和关闭授权后隐藏旧值。
- 安全回归使用 sentinel 凭证跑通真实 client、service、SQLite 和 HTTP API，确认凭证不出现在数据库、settings、API 或日志。
- Code Review：独立 Reviewer 发现重定向认证头边界、并发测试时序、旧快照错误提示和安全测试缺口；全部修复后复审通过。
- Commit：`accba4e feat(quota): add Codex subscription limits`
- 推送分支：`main`

## 里程碑 5：第一期整体收尾

已完成：

- 修复 total-only 累计快照在增量扫描中的重复计数，并持久化 last-only 到 cumulative 的待对账状态；五类 token 明细在对账后保持准确。
- 失败扫描不再刷新最后成功时间，旧 generation 保持可读，遗留 staging 数据会被清理。
- 扫描使用固定文件快照；计划后的纯追加延后到下一轮处理，截断、替换或非追加改写会明确失败且不污染现有数据。
- 本地诊断和 CLI 保留可操作的目标文件路径，HTTP API 对错误内容继续脱敏。
- README 的安装、单命令启动、扫描、配额授权和验证步骤已与最终实现一致。

最终验证记录：

- `make dev`：前后端可由一条命令同时启动。
- 浏览器端到端：Dashboard 通过真实 API 展示 SQLite 中的 180 tokens、2 条事件和五类非重叠明细；Diagnostics 显示 `Ready`、parser v2，并成功执行无新增的手动增量扫描。
- API 验收：health、summary、timeline、breakdown、dashboard、snapshot、diagnostics、quotas 和 settings 均返回 HTTP 200。
- 持久化验收：复用同一 SQLite 重启后，初始化时间保持 `2026-07-31T04:55:52Z`，2 条 usage event 保持不变，启动扫描为无新增的 incremental。
- 当前机器真实数据：最终 Reviewer 使用临时 SQLite 全量扫描 43 个 Codex 文件，解析 3493 条记录并去重为 1668 条；后续两次增量扫描正常且无 warning。
- `make verify`、`go vet ./...`、`go test -race ./...`、随机顺序全量测试 20 次和 `git diff --check`：通过。
- 关键 provider 与 scan 包重复测试 100 次，SQLite、HTTP API 与 CLI 重复测试 50 次：通过。
- Code Review：独立 Reviewer 复核累计 token、pending 对账、失败保旧、staging 清理、错误脱敏和并发文件快照；所有发现均已修复，最终无剩余 P0/P1/P2 问题。
- Commit：`09d822e fix(usage): harden incremental scan accuracy`
- 推送分支：`main`

## 里程碑 6：中文桌面界面与 Token 热力图

已完成：

- 将通用界面文案调整为中文，保留 Input、Cache、Output、Reasoning 等必要技术术语。
- 概览统一使用 `1D`、`7D`、`30D` 和 `ALL`，所有用量统计始于 `2026-07-01`。
- Dashboard API 在同一份 SQLite 快照中返回完整活动序列，前端展示 GitHub 风格的 53 周 Token 热力图、累计 token、活跃天数和最长连续天数。
- 采用适合 macOS 桌面浏览器的 Apple 风格视觉系统，并为有数据的热力图日期提供键盘聚焦、中文标签和可视精确值。
- 集中维护 API 路径、服务端地址、统计起始日和重复视觉值；开发规则补充了适度避免散落硬编码的要求。

验证记录：

- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check`：通过。
- Analytics 与 HTTP API 连续测试 50 次通过，前端 TypeScript 检查和 Vite 生产构建通过。
- macOS 桌面浏览器端到端：`1D` 为 180 tokens，`7D`、`30D`、`ALL` 均为 540 tokens；热力图固定读取统一统计起始日至今的 540 tokens，页面无横向溢出和 JavaScript 控制台错误。
- Code Review：独立 Reviewer 发现热力图初始定位、键盘详情和重复视觉色值 3 个 P2；全部修复后复审通过，无剩余 P0/P1/P2。
- Commit：`b5a4875 feat(dashboard): add Chinese token heatmap`
- 推送分支：`main`

## 里程碑 7：Token 数量级与默认配额

已完成：

- Token 紧凑值统一使用英文 `K`、`M`、`B`、`T` 数量级，并集中到一个格式化入口。
- 总量、分类、趋势、热力图和模型/项目排行均保留带千位分隔符的精确值或精确值提示。
- Codex 订阅配额在设置文件或字段不存在时默认开启；用户显式关闭后继续持久化并阻止自动刷新、API 与 CLI 绕过。
- README 和开发指南已同步默认行为、CLI 约束与 Token 显示口径。

验证记录：

- 格式化实测：`1200 → 1.2K`、`1250000 → 1.3M`、`2300000000 → 2.3B`、`4500000000000 → 4.5T`。
- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check`：通过。
- 设置、配额、HTTP API 和 CLI 连续测试 50 次通过；前端 TypeScript 检查和 Vite 生产构建通过。
- Code Review：独立 Reviewer 发现精确值提示和 README 旧授权措辞 2 个 P2；全部修复后复审通过，无剩余 P0/P1/P2。
- Commit：`4afa3a0 feat(app): improve token and quota defaults`
- 推送分支：`main`

## 里程碑 8：单进程生产运行

已完成：

- `make build` 会清理旧前端产物、执行 Vite 生产构建，并通过 production build tag 把页面资源嵌入当前 Mac 可运行的 `bin/dora`。
- `./bin/dora serve` 由一个 Go 进程同时提供 React 页面、`/api/v1/*` 和 SQLite，运行时不依赖 Node.js、npm、Vite、仓库源码或 `frontend/dist`。
- HTTP 层通过可选 `fs.FS` 注入前端资源；开发构建保持纯 API 模式，`make dev` 继续使用 Vite 同源代理。
- API 路由优先于 SPA fallback；前端路由可回退到 `index.html`，带扩展名的缺失资源、未知 API 和路径穿越均返回 404。
- 嵌入资源的暂存目录、前端构建目录和 `bin/` 均由 `.gitignore` 排除，构建失败也会清理暂存资源和旧二进制。
- README 区分开发模式、生产模式、构建依赖和运行依赖，并说明当前手动启动与后续菜单栏、LaunchAgent 的边界。
- macOS 菜单栏、LaunchAgent 常驻和登录后自动启动仍待后续里程碑实现，本次没有提前引入相关代码。

验证记录：

- 在没有 `frontend/dist` 和嵌入暂存目录的干净状态下，`go test ./...` 与 `go vet ./...` 通过。
- `make verify`、`go test -race ./...` 和 `git diff --check` 通过；前端 TypeScript 检查、Vite 生产构建和 production tag Go 构建均成功。
- 生产冒烟测试从仓库外目录启动 `bin/dora`，使用临时 SQLite、空 Codex home、未占用 loopback 端口和不含 Node/npm/Vite 的 `PATH`；根页面、真实静态资源、health API、SPA fallback、未知 API 与缺失 JS 均符合预期。
- 冒烟进程没有启动子进程，收到 SIGTERM 后正常退出；临时数据库只写入测试目录。
- 故意制造 Go 构建失败后，嵌入资源暂存目录和旧 `bin/dora` 均已清理。
- Code Review：独立 Reviewer 发现缺少 `index.html` 的内存文件系统测试缺口；补充回归测试并复审后，无剩余 P0/P1/P2/P3 问题。

## 里程碑 9：macOS 系统代理适配

已完成：

- Codex 配额请求优先遵循标准 `HTTPS_PROXY`/`NO_PROXY`，未配置时动态读取 macOS 当前固定 HTTPS 或 SOCKS 系统代理。
- Clash Verge、ClashX 和 Shadowrocket 通过统一的 macOS 系统代理或 TUN/VPN 路由适配，不识别应用进程、不依赖安装路径，也不写死代理端口。
- 配额地址、OAuth header、超时、重定向限制和失败保留旧快照等安全边界保持不变。
- 开发规则要求测试结束后正常关闭 Go、Vite 和生产服务，并确认测试端口已释放。

验证记录：

- 当前 Mac 直连 ChatGPT 配额地址超时，经 Clash Verge 暴露的系统代理可立即连通；修复后使用真实 Codex OAuth 和临时 SQLite 成功读取配额窗口。
- 单元测试覆盖 Clash Verge HTTPS、ClashX HTTPS、Shadowrocket SOCKS、环境变量优先、`NO_PROXY`、系统代理 fallback、无代理直连和非法配置。
- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check` 通过；provider 重复测试和 Linux amd64 交叉构建通过。
- 生产服务使用临时 SQLite 和临时 Codex home 通过真实系统代理进入 quota `ready` 状态；验收后服务正常退出，测试端口均已释放。
- Code Review：独立 Reviewer 复核代理语义、`scutil` 超时与进程回收、OAuth 安全边界和测试隔离，无 P0/P1/P2/P3 可行动问题。

## 里程碑 10：Token API 等价费用估算

已完成：

- 增加随程序嵌入的版本化 JSON 定价目录，记录 USD 单位、OpenAI 官方来源、逐模型价格和核对日期；更新定价不需要 migration、重扫 transcript 或改写 SQLite。
- 支持模型 ID、显式 alias 和最长 snapshot prefix 匹配；当前覆盖 GPT-5.6 Sol/Terra/Luna、GPT-5.5、GPT-5.4 和 GPT-5.3-Codex。
- 按五类非重叠 token 计算 Input、Cache read、Cache creation、Output 和 Reasoning 费用；Reasoning 使用 output 价格，GPT-5.6 cache write 使用 uncached input 的 `1.25x`。
- 未知模型、只有 total 的记录和已知模型中无法分类的 total gap 保持未定价；API 返回已定价 token、未定价 token、覆盖率、分类费用、官方来源和核对日期。
- Dashboard 新增 API 等价美元费用卡片，展示已定价部分、分类费用、覆盖率、未完整定价模型和限制说明，明确它不是 Codex 订阅实际账单。
- README 和开发指南记录定价更新流程；运行时不抓取官网 HTML，未来自动更新只接受经过校验的版本化签名清单。

验证记录：

- OpenAI 今日官方模型页与 Compare 页逐项复核当前价格；独立 Reviewer 发现初版 Terra/Luna 使用了 3 天旧搜索索引价格，已改为今日页面现价并增加逐模型价格回归断言。
- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check` 通过；前端 TypeScript 检查、Vite 生产构建和 production Go 构建通过。
- 真实数据桌面端验收：7D 页面从统一 Dashboard API 读取 `230.9M` token，展示已定价部分 `$150.40`、定价覆盖 `92.1%`、官方来源和未完整定价模型；刷新和切换 `1D` 后状态正常，控制台无警告或错误。
- 端到端使用临时复制的 SQLite，未修改用户原数据库；验收后生产服务正常退出，测试端口和临时数据均已清理。
- Code Review：首轮 1 个 P1 已修复；复审确认五类计价无重复、未知与 total gap 处理诚实、覆盖率和溢出边界正确，无剩余 P0/P1/P2/P3。

## 里程碑 11：单进程 macOS 菜单栏

已完成：

- 增加 `dora menubar [--addr --db --codex-home]`，由一个进程同时运行 AppKit 状态项、现有 HTTP/API、内嵌 React、SQLite、扫描器和配额服务。
- `serve` 与 `menubar` 复用同一个 application runtime；端口会在后台任务和菜单启动前绑定，冲突时返回明确错误，不终止旧进程也不改用其他端口。
- 菜单展示今日、7 日、全部 token、最高用量模型、Codex 5 小时/7 日配额和状态，并支持非并发异步刷新、打开实际 loopback 仪表盘和正常退出。
- 手动刷新先更新 token，再按授权刷新配额；配额失败时保留新 token 数据，菜单状态明确提示部分失败。
- 使用内嵌单色 template icon 和 accessory activation policy，不创建窗口或 Dock 图标。

验证记录：

- 单元与集成测试覆盖命令参数、共享 runtime、实际监听地址、端口冲突、HTTP/内嵌页面、格式化、空模型、配额正常/过期/关闭/未登录/错误、刷新禁用、部分失败和打开仪表盘参数。
- 当前 Mac 使用临时 SQLite、空 Codex home 和未占用端口启动生产菜单栏；确认只有一个 Dora PID、无子进程和 Node 进程，同一 PID 提供 health、snapshot 和嵌入页面，SIGTERM 后正常退出并释放端口。
- 自动化 GUI 工具无法枚举 macOS 状态栏 extra，图标可见性需在菜单栏中人工确认；进程 activation policy、无窗口实现和状态项资源已通过代码与运行时检查。
- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check` 通过；菜单并发与生命周期测试重复运行通过。
- Code Review：独立 Reviewer 发现异步 view 提交竞态、token 单位临界舍入、退出 wiring 测试缺口和刷新中操作状态 4 个问题；全部修复并复审通过，无剩余 P0/P1/P2/P3。

## 里程碑 12：当前用户 LaunchAgent 生命周期

已完成：

- 增加顶层 `dora install`、`dora status` 和 `dora uninstall`，只管理当前 macOS 用户的 `gui/<uid>` LaunchAgent，不使用 sudo 或系统级 daemon。
- install 校验生产 Web 资源，将当前二进制和 plist 原子安装到稳定用户路径，并按现代 `launchctl print/bootout/bootstrap/kickstart` 顺序幂等加载同一个 `dora menubar` 进程。
- plist 固定使用 `io.github.wubh576.dora`，配置 RunAtLoad、仅异常退出恢复、10 秒重启节流和明确的 stdout/stderr 日志路径，不包含凭证。
- status 综合安装文件、launchd 加载/运行状态和真实 loopback health；退出码 0、1、2 分别表示正常、未运行类状态和检查自身失败。
- uninstall 幂等停止 LaunchAgent，只删除 plist、安装二进制和明确临时文件，保留 SQLite、settings、日志与 Codex 原始数据。

验证记录：

- 注入临时用户目录、当前二进制、UID、文件系统、命令 runner 和 health checker，自动测试未访问真实 LaunchAgent 或真实用户安装路径。
- 测试覆盖路径、合法 plist、原子权限、生产资源检查、首次/重复安装、更新顺序、状态分类与退出码、launchctl 错误、参数化命令、卸载幂等和用户数据保留。
- 当前 Mac 真实 install 后，plist 通过 `plutil`，`launchctl print` 显示用户 `gui/501` job 正常运行；同一 PID 提供菜单栏、8080 health 和嵌入页面，无子进程或 Node 进程。
- 重复 install 首轮暴露 bootout 后 launchd 尚未完成清理的时序问题；增加明确的卸载等待后，重复安装可安全更换 PID 且始终只有一个 Dora 进程。
- 使用临时数据库启动手动 `dora serve` 占用 8080 时，install 会在修改 LaunchAgent 前明确拒绝，不会终止现有进程或被旧 health 误导；测试服务随后正常退出并清理。
- 真实 uninstall 后 status 返回 1，launchd job、plist、安装二进制和临时文件均消失，8080 释放，既有数据库 inode 保持不变；重复 uninstall 成功。
- 最终生产二进制已重新安装并留驻运行；status 返回 0，LaunchAgent PID 37792 为唯一 Dora 进程且无子进程，安装副本与构建产物一致，health 与嵌入首页均返回 200。
- Code Review：独立 Reviewer 发现 health 与目标 job 未绑定、README 主动退出语义和失败恢复指引 3 个问题；全部修复并复审通过，无剩余 P0/P1/P2/P3。

## 里程碑 13：构建来源与启动环境信息

已完成：

- 增加统一 build info，覆盖 Git commit、`dirty/clean`、UTC build time、Go version、`GOOS/GOARCH` 和 macOS product version。
- Dora 不维护独立产品版本号，也不要求 Git tag；`make build` 和 `make dev` 通过 `-ldflags` 注入 commit、工作区状态和构建时间，12 位短 commit 作为构建标识。
- `dora status` 展示构建标识和完整环境信息；运行时启动日志记录同一份非敏感信息，不提供独立 `dora version` 命令。

验证记录：

- build info 格式、缺省值、短 commit、`dirty/clean`、status 输出与启动日志均有自动测试。
- 生产二进制在脏工作区构建时标识追加 `dirty`，提交后重建则只显示当前短 commit。
- 真实临时服务的 health API 返回与启动日志一致的运行中 build info，测试结束后进程和端口均已清理。
- 启动日志不记录用户名、设备序列号、OAuth token 或其他凭证。
- Code Review：独立 Reviewer 发现 `status` 混用了命令版本与 LaunchAgent 运行状态；改为通过 health API 读取运行中实例并明确标注来源后复审通过，无剩余 P0/P1/P2/P3。

## 里程碑 14：后台失败日志真实原因

已完成：

- Codex 用量自动扫描和配额自动刷新失败日志在单行内包含 provider、操作、底层错误原因以及重试建议或影响范围。
- 正常 context cancellation 与 Dora 主动退出不记录为后台失败，原有成功日志和扫描、配额业务流程保持不变。
- 配额 provider 保留安全的底层文件、JSON、HTTP 状态和网络 error chain，不记录请求 Header、OAuth token、control token、Cookie 或响应正文。

验证记录：

- 使用 sentinel error 验证扫描和配额日志包含真实原因，换行错误被压成单行，context cancellation 不产生误导日志。
- 配额网络错误测试确认 error chain 可追踪且不包含 fixture access token 或 account ID；测试未读取真实凭证或访问网络。
- `make verify`、相关 race 测试、`go vet` 和 `git diff --check` 通过，前端生产构建与 production Go 构建通过。
- Code Review：独立 Reviewer 发现延迟 context cancellation 会误吞成功日志；修复后补充成功日志回归测试，复审确认无剩余 P0/P1/P2/P3。

## 里程碑 15：LaunchAgent 日志自动轮转

已完成：

- LaunchAgent stdout 和 stderr 各自以 200 MiB 为活动文件阈值，启动时检查一次，运行中每 10 分钟检查一次。
- 达到或超过阈值后，当前内容原子覆盖同名 `.1` 备份，再 truncate 原活动文件；只保留一个备份，已打开的 append 文件描述符继续写入原路径。
- 两个日志独立处理，缺失或未达阈值时无操作；一侧失败只记录原因并等待下次重试，不影响另一侧和 Dora 主流程。
- 轮转任务由共享 runtime context 管理，只有 launchd service、安装二进制和 stdout/stderr inode 均与 Dora plist 匹配时才启用；手动 `serve`、普通 `menubar`、伪加 `--launchagent` 和 uninstall 不操作或删除日志。

验证记录：

- 临时目录自动测试覆盖低于、等于和超过阈值，轮转后打开描述符继续写入活动路径，覆盖 `.1`、不生成 `.2`、双流独立、缺失文件与单侧失败继续处理。
- 周期检查使用可注入的小阈值和毫秒级周期，验证 runtime 启动检查及 context cancellation 后退出；生产常量回归确认 200 MiB 和 10 分钟。
- `make verify`、`go test -race ./...`、`go vet ./...` 和 `git diff --check` 通过；前端 TypeScript、Vite production build 和 production Go 二进制构建通过。
- 真实 macOS production 冒烟使用临时 HOME、稳定安装路径、SQLite、Codex 目录和日志，以 2 KiB 阈值验证启动检查和运行期检查；stdout/stderr 活动 inode 始终不变，`.1` 内容被正确覆盖，活动 stderr 继续收到当前进程的轮转成功日志，目录始终只有两个活动文件和两个备份。
- 冒烟期间菜单栏进程保持运行，独立 loopback health 返回 HTTP 200；结束后进程正常退出、端口释放，临时二进制、数据库和日志全部清理，未触碰真实用户数据或设置。
- Code Review：独立 Reviewer 发现手动伪加 `--launchagent` 可误启轮转，以及随机临时文件在强制退出后可能累积；增加 service、安装路径和日志 inode 三重门禁并改用可恢复的固定 `.1.tmp` 后，复审确认无剩余 P0/P1/P2/P3。

## 里程碑 16：Provider 隔离与 Claude Code 只读采集

已完成：

- 新增 `provider.claude-code` discovery/parser/dedup，默认只读发现主 transcript 与 `subagents/agent-*.jsonl`，支持 `CLAUDE_CONFIG_DIR` 和重复 `--claude-home`。
- 只解析 assistant `message.usage`，保留 transcript 实际 model、五类 token、streaming largest-wins、fork replay 去重、subagent 新 usage 和 config home 隔离。
- SQLite migration 4 只增加 provider 聚合 diagnostics；usage generation 与 checkpoint 按 provider 原子隔离。不建立 session 表，不保存 session ID、父子关系或完整项目路径。
- 自动与手动扫描同时覆盖 Codex/Claude；任一 provider 失败保留各自上一次成功数据，另一个 provider 仍完成提交。

验证记录：

- 脱敏 fixtures 覆盖 streaming zero/partial/final、fork、subagent、cache、原生 reasoning、Anthropic thinking carve、非 Anthropic 不 carve、模型切换、半行、append/replace、缺失目录、非法和溢出 token。
- v3 SQLite 自动升级到 migration 4 后既有 Codex usage 和 quota 保持不变；新库明确不存在 `agent_sessions` 表。
- 当前 Mac 真实只读扫描覆盖 9 个 Claude Code transcript、8 个主 session 和 68 条去重 usage；紧接着增量扫描解析 0 条新增，结果仍为 68 条。
- Code Review：两个独立 Reviewer 分别检查 provider generation 和 Claude collector；已修复 run/provider 错配、原始 checkpoint 路径、配置消失误清空、缺失 message ID 的不稳定去重及错误路径泄漏，复审无剩余 P0/P1/P2/P3。

## 里程碑 17：多 Provider 统一展示

已完成：

- summary、timeline、breakdown、dashboard 和 snapshot 默认合并 Codex 与 Claude Code，固定返回两个 provider 的独立 token、事件数和模型分布。
- `provider` 与 `provider_model` breakdown 保留来源归属，同名模型不会丢失 provider 信息。
- Web 概览显示合计和两个 provider 卡片；无 Claude Code 会话时明确显示空态。Diagnostics 分别展示配置发现、聚合 session 数、文件数、事件数、parser 版本和扫描状态，不暴露 session 或完整路径。
- 菜单栏继续显示统一 snapshot，用量包含两个 provider；5 小时和 7 日订阅配额明确标记为 Codex。
- 单个 provider 扫描失败只影响自己的诊断和错误提示，另一个 provider 的最后成功用量继续进入合计。

验证记录：

- Analytics、SQLite、HTTP API 和菜单栏测试覆盖 Codex-only、Codex + Claude Code 精确合计、同名模型来源、provider 诊断、snapshot 与 Codex quota 标签。
- 脱敏 production 浏览器冒烟显示合计 182 tokens、Codex 125、Claude Code 57；两个 provider 使用同名模型时仍分别展示。Diagnostics 增量扫描后维持 2 条事件，浏览器控制台无 warning/error。
- 当前 Mac 使用真实目录和临时 SQLite 只读扫描 62 个 Codex transcript 与 9 个 Claude Code transcript，分别保存 3794 与 68 条去重事件；health、summary、timeline、provider breakdown、provider-model breakdown、dashboard、snapshot、diagnostics 和内嵌页面全部成功。
- 无 Claude Code 目录的 production 冒烟正常启动；Claude diagnostics 为 `not_found`，Web dashboard 与菜单栏 snapshot 保持可用并返回固定的零值 provider 项。
- `make verify`、`go vet ./...`、`go test -race ./...` 和 `git diff --check` 通过；前端 TypeScript、Vite production build 和嵌入式 Go 二进制构建通过。所有临时服务、端口、fixture 和 SQLite 均在验收后清理。
- Code Review：独立 Reviewer 发现合并 snapshot 新鲜度可能被较新的 provider 掩盖、Claude 历史 generation 会遮住当前无会话空态，以及 Web/菜单栏 DTO 文档不准确；改为按参与合计的最旧扫描计算 stale、独立展示当前 Claude 空态并修正文档后复审通过。

## 里程碑 18：统一统计起始日

已完成：

- 将 Dashboard、API、热力图和菜单栏“全部”用量的统一起点调整为 `2026-07-01`。
- 今日、7 日和 30 日仍按 macOS 本地日历边界计算，只在范围早于统一起点时截断。
- 菜单栏 token 继续合并 Codex 与 Claude Code；5 小时和 7 日配额继续只表示 Codex。
