# 架构与技术决定

## M0 调用路径

Wails 3 在窗口中加载由 Vite 编译、Go `embed` 打包的静态文件。Vue 页面通过同版本 Wails runtime 调用 Go 的 `Workspace` 服务。桌面服务读写当前用户的 SQLite 数据库。Go 核心与持久化层可在没有 GUI、Wails 或云 SDK 的测试进程中使用。

| 位置 | 职责与边界 |
| --- | --- |
| `main.go` | 创建应用与窗口，注册服务，在关闭时释放数据库。应用数据保存在操作系统返回的用户配置目录下的 `S3S/workspace.sqlite3`。 |
| `workspace.go` | 提供连接列表、保存本地连接、读取任务等元数据接口。返回可区分的结果和错误。保存配置只表示本地写入完成。 |
| `internal/core` | 定义独立的连接、厂商能力、传输选项、任务状态、检查点和结构化错误。能力表反映已实现操作。M0 中所有实际云操作能力关闭。 |
| `internal/store` | 管理数据库迁移、连接配置、任务、远程会话标识及分片检查点。采用 `database/sql`、嵌入的 SQL 和支持 CGO 的 SQLite 驱动。 |
| `frontend/src` | 管理页面、用户输入、界面语言和 IPC。浏览器预览提示缺少桌面连接。连接配置与任务从桌面服务取得。 |
| `scripts/`、`.github/workflows/` | 以相同检查和打包入口在原生系统完成构建。产物使用 Wails 的 `production` 构建标签和 Go 模块，包含已编译的 UI。 |

## 依赖与构建选择

| 组件 | M0 锁定值 | 依据或使用位置 |
| --- | --- | --- |
| Go | 1.27.4 | 根目录 `go.mod` 与 CI；本地数据库驱动依赖 CGO 和原生 C 编译器。 |
| Wails Go 模块、CLI、前端 runtime | 3.1.0 | Go 使用 `github.com/wailsapp/wails/v3`；CLI 入口为相同模块的 `cmd/wails3`；前端使用 `@wailsio/runtime`。打包脚本直接构建 Go 入口，无需 Wails v2 命令。 |
| SQLite 驱动 | `github.com/mattn/go-sqlite3` v1.14.53 | 使用驱动内嵌的 SQLite 编译；版本与校验和见 `go.mod` 和 `go.sum`。 |
| Node.js、npm | 24.22.3、11.19.1 | CI 使用精确的 Node.js 版本；前端声明 npm，使用 `npm ci` 安装锁文件指定的依赖。 |
| Vue、TypeScript | 3.5.46、6.0.3 | Vue 页面与类型检查。单独试用 TypeScript 7.0.2 时，vue-tsc 3.3.14 加载 TypeScript 的 lib/tsc 导出失败，因此固定兼容的 6.0.3。 |
| Vite、plugin-vue、vue-tsc | 8.3.8、6.0.11、3.3.14 | `frontend/package.json` 固定直接版本；传递依赖及完整性摘要保存在 `frontend/package-lock.json`。 |

版本可在 [Go 发布记录](https://go.dev/dl/)、[Wails v3 模块元数据](https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/v3.1.0.mod)、[npm runtime 元数据](https://registry.npmjs.org/@wailsio/runtime/3.1.0) 核对。Wails v3.1.0 模块声明最低 Go 1.26.0。Linux 构建基于 GTK 4 与 WebKitGTK 6.0，Windows 使用原生编译器环境和 WebView2，macOS 使用 Xcode 工具链与系统 WebKit。具体安装与检查入口见 [README.md](../README.md)。

## 状态与持久化

第一个迁移以单一事务创建 `connections`、`tasks` 和 `parts`，并更新 `PRAGMA user_version`。重复打开已迁移库时复用原有记录。遇到未知模式版本时返回错误。启用外键、WAL 和 FULL 同步。连接记录只含公开配置，端点禁止用户信息、查询和片段。

任务记录保存方向、连接 ID、桶、完整 Key、本地路径、长度、状态和上传 ID。Key 作为普通文本传递。分片以任务 ID 和分片编号定位，记录偏移、长度、ETag 及校验值。比较预期状态再更新可阻止迟到的旧状态写入覆盖新状态。提交检查点时校验区间、会话和冲突；相同确认可重放。

后续调度器需要在启动时处理崩溃遗留的运行中任务，核对本地文件身份、远端对象版本、分片会话及有效区间，再决定续传、重新排队或请求用户处理。当前数据层提供检查点基础，四种中断场景的端到端恢复属于 M1。

## 语言与错误

两个内置平面词典位于 `frontend/src/locales`。UI 根据当前语言解释状态及错误，在显示阶段展开具名占位符。设置页导入的 JSON 结构如下：

```json
{
  "locale": "fr",
  "name": "Français",
  "messages": {
    "nav.connections": "Connexions",
    "connections.count": "{count} profils"
  }
}
```

导入时校验语言标识、已知 key 和占位符；缺失条目回退到英语。语言偏好及导入词典保存在 WebView 本地存储。响应式更新只作用于视图。M0 保留原生菜单、托盘、通知文案 key；应用自定义菜单项、托盘图标和系统通知的行为及语言切换验收留待接入相应组件时完成。

核心错误保留稳定码、安全参数、厂商码、请求标识和可重试性。M0 的桌面调用区分本地存储失败与输入错误。后续适配器需在跨越 IPC 前提取允许的诊断字段并关联重试历史，避免传播带签名的 URL、请求头或响应正文。

## 后续传输引擎

- Go 调度器在应用范围内持有一个上传预算、一个下载预算。所有用户文件、账号、分片、重试和测速请求复用对应方向的预算。大文件在本地流与 SDK 之间传输，IPC 仅传递元数据、命令、进度和结果。
- AWS 与 R2 共用 S3 协议适配层，独立配置区域、端点、寻址方式和能力。OSS、TOS 先评估各自原生 SDK 的分页、校验与分片恢复，再分别锁定版本。通用 S3 入口按实际兼容行为声明能力。M0 尚无云 SDK 或凭据读取路径。
- R2 优化应在仅适用于目标端点的 Go HTTP 拨号层生效，配合带过期时间的探测结果、手动覆盖、失败回退。保留认证时使用的请求域名、TLS SNI 及证书校验。测速与正式上传共享调度器、限速器和远端清理路径。
