# Dora 第一期实现状态

本文档记录 Dora 第一期各里程碑的实现状态、验证结果和对应提交。只有功能、测试、端到端自测、Code Review、提交与推送全部完成后，里程碑才标记为完成。

## 里程碑概览

| 里程碑 | 状态 | Commit |
| --- | --- | --- |
| 1. 基础运行链路 | 已完成 | `68db287` |
| 2. Codex 本地用量采集 | 已完成 | `738d1a9` |
| 3. Token 统计 API 与 Web 仪表盘 | 进行中 | - |
| 4. Codex 订阅配额 | 未开始 | - |
| 5. 第一期整体收尾 | 未开始 | - |

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

当前状态：进行中。

## 里程碑 4：Codex 订阅配额

当前状态：未开始。

## 里程碑 5：第一期整体收尾

当前状态：未开始。
