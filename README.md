# Dora

Dora 是一个运行在 macOS 本地的 AI 编程用量管理工具。当前最小版本先打通 React 前端、Go 后端和 SQLite，确认基础链路可以真实运行。

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

## 验证

```bash
make verify
```

该命令会运行后端测试并构建前端。
