# 架构与技术决定

## 桌面与存储边界

Wails 3 在窗口中加载由 Vite 编译、Go `embed` 打包的静态文件。Vue 页面通过同版本 Wails runtime 调用 Go 的 `Workspace` 服务。桌面服务读写当前用户的 SQLite 数据库，按连接选择凭据与云适配器。Go 核心与持久化层可在没有 GUI、Wails 或云 SDK 的测试进程中使用。

| 位置 | 职责与边界 |
| --- | --- |
| `main.go` | 创建应用与窗口，注册服务，在关闭时释放数据库。应用数据保存在操作系统返回的用户配置目录下的 `S3S/workspace.sqlite3`。 |
| `workspace.go` | 提供连接列表、保存本地连接、读取任务等元数据接口。返回可区分的结果和错误。保存配置只表示本地写入完成。 |
| `internal/core` | 定义独立的连接、厂商能力、传输选项、任务状态、检查点和结构化错误。能力表反映已实现操作。AWS/R2 使用独立配置和共同的 S3 协议适配器；其余厂商保留公开连接模型。 |
| `internal/vault`、`internal/s3client` | 按当前连接在系统凭据库读取授权信息，使用固定版本的 AWS SDK 构造签名请求、解析分页和条件范围响应；对外提供可本地化的错误。 |
| `internal/transfer` | 处理下载区间与上传分片，核对源身份、计算摘要、提交检查点，协调本地或远端发布与恢复。 |
| `internal/store` | 管理数据库迁移、连接配置、任务、远程会话标识及分片检查点。采用 `database/sql`、嵌入的 SQL 和支持 CGO 的 SQLite 驱动。 |
| `frontend/src` | 管理页面、用户输入、界面语言和 IPC。浏览器预览提示缺少桌面连接。连接配置与任务从桌面服务取得。 |
| `scripts/`、`.github/workflows/` | 以相同检查和打包入口在原生系统完成构建。产物使用 Wails 的 `production` 构建标签和 Go 模块，包含已编译的 UI。 |

## 依赖与构建选择

| 组件 | 锁定版本 | 依据或使用位置 |
| --- | --- | --- |
| Go | 1.27.4 | 根目录 `go.mod` 与 CI；本地数据库驱动依赖 CGO 和原生 C 编译器。 |
| Wails Go 模块、CLI、前端 runtime | 3.1.0 | Go 使用 `github.com/wailsapp/wails/v3`；CLI 入口为相同模块的 `cmd/wails3`；前端使用 `@wailsio/runtime`。打包脚本直接构建 Go 入口，无需 Wails v2 命令。 |
| SQLite 驱动 | `github.com/mattn/go-sqlite3` v1.14.53 | 使用驱动内嵌的 SQLite 编译；版本与校验和见 `go.mod` 和 `go.sum`。 |
| AWS SDK for Go v2 | 核心 v1.48.0、credentials v1.21.3、S3 v1.116.0、Smithy Go v1.28.10 | AWS 和 R2 的签名、请求与协议错误解码。按模块独立锁定并提交校验和。 |
| 跨进程工作区锁 | `github.com/gofrs/flock` v0.13.2 | 桌面进程独占当前配置目录的 `workspace.lock` 后才能恢复运行中任务。 |
| 系统凭据存储 | `github.com/zalando/go-keyring` v0.2.8 | macOS Keychain、Windows Credential Manager、Linux D-Bus Secret Service。 |
| Node.js、npm | 24.22.3、11.19.1 | CI 使用精确的 Node.js 版本；前端声明 npm，使用 `npm ci` 安装锁文件指定的依赖。 |
| Vue、TypeScript | 3.5.46、6.0.3 | Vue 页面与类型检查。单独试用 TypeScript 7.0.2 时，vue-tsc 3.3.14 加载 TypeScript 的 lib/tsc 导出失败，因此固定兼容的 6.0.3。 |
| Vite、plugin-vue、vue-tsc | 8.3.8、6.0.11、3.3.14 | `frontend/package.json` 固定直接版本；传递依赖及完整性摘要保存在 `frontend/package-lock.json`。 |

版本可在 [Go 发布记录](https://go.dev/dl/)、[Wails v3 模块元数据](https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/v3.1.0.mod)、[npm runtime 元数据](https://registry.npmjs.org/@wailsio/runtime/3.1.0) 核对。Wails v3.1.0 模块声明最低 Go 1.26.0。Vite 8.3.8 声明 Node.js ^20.19.0 或 >=22.12.0；本工程使用已验证的 Node 24.22.3。Linux 构建基于 GTK 4 与 WebKitGTK 6.0，Windows 使用原生编译器环境和 WebView2，macOS 使用 Xcode 工具链与系统 WebKit。具体安装与检查入口见 [README.md](../README.md)。

## 云请求与凭据边界

连接的公开配置落入 SQLite。账号表单通过桌面服务先提交公开配置，再将用户明确填写的访问密钥及可选会话令牌写入系统凭据库。两个存储分别报告结果；若仅凭据写入失败，返回已经保存的账号 ID 供重试。五类账号都可保存凭据，只有已实现的适配器能使用它们发出云请求。每个连接 ID 有独立凭据条目；读取时核对存入时绑定的厂商、区域和端点。修改连接指向后，新端点需要单独授权；读取列表和任务不会返还凭据。Linux 需要可用的会话 D-Bus 与已解锁的 Secret Service；密钥库不可用时返回本地化错误。平台也会限制单个条目大小；包含较长临时令牌的凭据若超过系统限制，保存操作会失败。上游实现见 [go-keyring v0.2.8](https://github.com/zalando/go-keyring/tree/v0.2.8)。

Go S3 适配器使用显式凭据。AWS 根据填写的区域生成官方 S3 HTTPS 端点（含中国区域）；区域留空时使用 `us-east-1`。R2 使用账号专属 HTTPS API 端点并按 `auto` 签名。两者均使用路径寻址，签名请求限定在该端点，禁用重定向；普通的自定义 S3 服务属于通用适配器范围。AWS 的桶分页使用远端游标，R2 兼容路径读取桶列表后按名字分页；可直接输入桶名以使用桶级授权。对象列表使用 `ListObjectsV2`、前缀和分隔符区分目录前缀与真实对象；按 S3 的 URL 编码约定解码 Key，保留字面加号、斜杠和点段。下载操作通过 Go 的 HEAD 与带 `If-Match`、可选版本 ID 的范围 GET 读取内容。检查 HTTP 206、Content-Range、长度和响应验证标识。收到的 ETag 用作对象标识；分片数据的 SHA-256 用于本地恢复校验。文件数据由 Go 写入磁盘。界面发起任务并接收元数据和进度；桌面服务只在用户主动提供凭据时将其写入系统凭据库。

## 状态与持久化

第一个迁移以单一事务创建 `connections`、`tasks` 和 `parts`，并更新 `PRAGMA user_version`。重复打开已迁移库时复用原有记录。遇到未知模式版本时返回错误。启用外键、WAL 和 FULL 同步。连接记录只含公开配置，端点禁止用户信息、查询和片段。

任务记录保存方向、连接 ID、桶、完整 Key、本地路径、长度、状态和上传 ID。Key 作为普通文本传递。第二个迁移增加下载清单，持久化厂商、区域、端点、ETag、版本 ID、临时文件路径、区间长度和最近一次结构化错误。M0 数据库升级后，缺少该清单的历史任务仍可查看，仅保存了完整下载身份的任务可续传。分片以任务 ID 和分片编号定位，记录偏移、长度、ETag 及校验值。比较预期状态再更新可阻止迟到的旧状态写入覆盖新状态。提交检查点时校验区间、会话和冲突；相同确认可重放。

下载任务绑定连接及原始端点、远端对象身份和用户选择的本地目标。引擎按 8 MiB 区间读取，在同目录的临时文件上完成相应区间写入后持久化 SHA-256 检查点。重启后的中断任务允许手动继续。恢复先核对远端身份，再检查本地已确认区间的摘要；本地数据损坏时需重读相应区间，远端身份变化时应提示冲突。最终通过硬链接将完整文件发布为用户选择的目标路径，要求该文件系统支持硬链接且目标路径仍然可用。

本轮上传支持普通文件。应用将 AWS 单文件大小限制为 5 TiB，R2 使用厂商列出的精确上限 5 TiB 减 5 GiB。默认每片 8 MiB，大文件按整 MiB 调大分片，最多 10,000 片。Go 依次取分片快照，常规分片留在内存，较大分片使用随句柄关闭而清理的临时文件。源文件需要多次读取，任务吞吐取决于本地 I/O 和网络条件。[R2 大小限制](https://developers.cloudflare.com/r2/platform/limits/)

上传在 SQLite v3 记录来源文件身份、长度、修改时间和 SHA-256，连同连接范围、分片大小、远端会话 ID、已确认分片、发布阶段及取消意图一起持久化。迁移保留 v1/v2 的数据。没有来源清单的历史记录仅用于查看。

对非空文件，引擎使用 `CreateMultipartUpload`、`UploadPart`、分页 `ListParts` 和 `CompleteMultipartUpload`。分片携带 `Content-MD5`；恢复时对照本地校验值与服务确认的编号、大小和 ETag。ETag 按厂商返回的标识处理。空文件使用条件 `PutObject`。提交时设置 `If-None-Match: *` 以保护同名对象。之后用 HEAD 核对任务标识、来源摘要元数据和实际长度。隔离验收另外读回实际对象并验证字节摘要。[AWS 条件写入](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html) · [R2 S3 API 兼容矩阵](https://developers.cloudflare.com/r2/api/s3/api/)

应用在发起创建或完成请求前持久化操作阶段。完成响应丢失时，通过任务标识识别已经发布的对象。取消只作用于任务绑定的上传会话。若服务已创建会话，响应却在保存会话 ID 前丢失，本地无法证明未知会话归属；应在桶中设置清理过期分片的生命周期规则。

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

核心错误保留稳定码、安全参数、厂商码、请求标识和可重试性。桌面调用区分本地输入、存储和凭据库错误。云适配器按状态区分无权访问、对象消失、远端变更及区间不符，从 SDK 中提取经过字符与长度限制的厂商错误码和请求标识。上传和下载任务持久化最近的结构化错误；完整脱敏重试历史属于后续工作。

## 后续传输引擎

- Go 调度器在应用范围内持有一个上传预算、一个下载预算。所有用户文件、账号、分片、重试和测速请求复用对应方向的预算。大文件在本地流与 SDK 之间传输，IPC 仅传递元数据、命令、进度和结果。
- AWS 与 R2 共用 S3 协议适配层，独立配置区域、端点与能力。OSS、TOS 先评估各自原生 SDK 的分页、校验与分片恢复，再分别锁定版本。通用 S3 入口按实际兼容行为声明能力。
- R2 优化应在仅适用于目标端点的 Go HTTP 拨号层生效，配合带过期时间的探测结果、手动覆盖、失败回退。保留认证时使用的请求域名、TLS SNI 及证书校验。测速与正式上传共享调度器、限速器和远端清理路径。
