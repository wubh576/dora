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

页面会通过 Vite 的同源代理调用真实后端 API，并展示：

- `Dora is running`
- `Backend connected`
- `SQLite ready`
- Dora 初始化时间

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

## 验证

```bash
make verify
```

该命令会运行后端测试并构建前端。
