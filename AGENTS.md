# S3S 开发约定

在网页版 Codex 的云端检出目录开发、测试与编译。先读 README.md、docs/STATUS.md 和 docs/ROADMAP.md，确认本次里程碑。使用开发分支提交 PR，保留既有改动。普通 PR 只需要源代码、版本锁和文档。禁止提交 node_modules、数据库、编译产物、签名材料或凭据。

## 设计与安全

- Go 核心及其测试独立于 Wails、GUI 和 WebView。前端通过窄接口传递意图、元数据和进度。文件内容、分片、校验、重试、取消和持久化由 Go 管理。
- 对象 Key 是厂商协议中的原始字符串。保留前后斜杠、点段、大小写、重复分隔符和 Unicode。生成本地下载文件名时单独处理本地安全边界。
- 任务、厂商上传标识和已确认分片需持久化。恢复必须先核对远端状态和本地源文件，在能证明对象完整后才能报告成功。应用总上传和总下载限速分别覆盖所有账号、文件、并发请求和重试。
- 正常模式展示真实持久化状态或明确的空状态。尚未接入的操作需说明可用性。测试夹具仅限测试或显式 Demo。
- 凭据走系统安全存储的引用。严禁将凭据或带签名 URL 放进源码、SQLite、WebView 持久化、图片或日志。后端提供稳定错误码、安全参数、厂商错误码与请求 ID，UI 负责本地化。诊断导出需要脱敏。
- 所有自有可见文本通过语言资源呈现，包括原生菜单、托盘和通知。变更两套内置语言时检查 key 与具名占位符。切换语言只更新视图，不重启 Go 服务或传输队列。
- 固定 Go、Node、Wails 与依赖版本，提交 go.sum 和前端锁文件。修改 Wails 时同步检查 Go 模块、CLI 和 npm runtime 的发行版本，并验证实际 API。新云 SDK 单独固定版本。

## 提交与验收

- 运行 README 中的云端检查命令。涉及 SQLite 时检查迁移、重开数据库后的内容和失败事务。涉及 UI 时检查中英文、空状态、键盘导航与 IPC 失败路径。
- PR 说明包含范围、基线、实际执行的命令、失败和跳过原因。按提交 SHA 核对四个原生 runner 的平台、架构和构建产物；检查结果更新到 docs/STATUS.md。编译、桌面启动、交互和真实云端操作分别记录。
- 不在普通 PR 触发需要云密钥的测试。后续真实存储验证按 docs/TESTING.md 使用隔离桶，控制流量并清理。仅在发行维护者授权后按 docs/RELEASING.md 签名和打标签。

在仓库根目录运行 `bash scripts/cloud-linux.sh setup`。执行 `export npm_config_cache=/workspace/.s3s-tools/npm-cache` 后运行 `bash scripts/check.sh frontend`、`bash scripts/cloud-linux.sh run bash scripts/check.sh go` 和 `bash scripts/cloud-linux.sh run bash scripts/build.sh`。原生 runner 的等价命令为 `bash scripts/check.sh` 和 `bash scripts/build.sh`，Linux 先执行 `bash scripts/setup-linux.sh`。

修改 AWS/R2 传输或恢复时，额外运行 `bash tests/integration/run.sh`，并在云端运行 `bash scripts/cloud-linux.sh run go test -race -mod=readonly -count=1 ./...`。验证桌面文件选择器时，将本次提交构建归档解出的 Linux 可执行文件路径作为集成脚本的第一个参数。验证语言和桌面工作区时，运行 `python3 tests/ui/preview.py` 和 `bash tests/ui/linux-container.sh /path/to/S3S`。隔离测试服务、原生窗口和真实厂商桶各自记录结果。


更改账号连接、对象操作或界面布局时，运行双语 Chromium 检查与 Linux 原生交互检查。浏览器检查包含 760×560、1024×768、1200×800 三档；原生检查使用 TLS 隔离服务测试获准与被拒绝的请求。变更厂商读取或限速时，还需运行竞态检查和 `bash tests/integration/run.sh`。打包时保持 Windows ZIP 根目录只有 `S3S.exe`，用 `scripts/check-archive.py` 核对文件，Actions 上传单个成品时使用 `archive: false`。
