# S3S

S3S 是用 Go、Wails 3、Vue 3 和 SQLite 构建的对象存储桌面客户端。当前开发分支在 M0 桌面工程上接入了 AWS S3 和 Cloudflare R2 的只读浏览与下载。支持本机多连接配置、任务记录、中英切换和语言包导入。构建和普通测试无需云账号。上传、远端复制与删除、OSS/TOS 传输及通用 S3 适配仍在路线图中。

## 使用 AWS S3 或 Cloudflare R2

1. 在桌面端「连接」页面保存连接的公开配置。AWS 填写桶所在区域，默认 `us-east-1`；R2 填写该账号的 HTTPS S3 API 端点，使用签名区域 `auto`。通用 S3 类型保留给后续适配。
2. 为该连接单独设置访问凭据。需要能访问当前用户的系统凭据库：Windows 使用凭据管理器，macOS 使用钥匙串，Linux 使用会话 D-Bus 上的 Secret Service。云端无桌面会话的构建容器可以运行普通检查。实际连接需要可用且已解锁的凭据库。
3. 在「对象」页面选择连接后，可以列出桶，也可直接输入已知桶名，再进入前缀或翻页。只授权单个桶的账号可直接打开该桶；浏览与下载分别需要该桶的列表和对象读取权限。
4. 下载时使用系统对话框选择一个尚未存在的目标文件。目标目录需可写、有足够空间，并支持创建硬链接。Go 引擎在目标目录内写临时文件，完成后发布到所选路径；目标文件已存在会报告冲突。
5. 在任务中心暂停或继续下载。程序意外退出后，重新打开相同用户配置，选中中断任务并手动继续。保留同目录的临时文件；恢复时会重新检查对象身份及已保存的数据块。更换显示语言只更新界面。

## 网页版 Codex 开发

在云端打开 `Cdners/S3S`，进入仓库根目录。当前实例为 Debian 13.7、Linux x86_64，提供 Docker、Node 24.22.3 和 npm 11.19.1。宿主的 `/usr/bin/go` 不是 Go 编译器。使用项目脚本准备固定版本工具链和带 GTK 4 / WebKitGTK 6 的 Linux 构建容器。

```bash
cd /workspace/S3S
bash scripts/cloud-linux.sh setup
export npm_config_cache=/workspace/.s3s-tools/npm-cache
bash scripts/check.sh frontend
bash scripts/cloud-linux.sh run bash scripts/check.sh go
bash scripts/cloud-linux.sh run bash scripts/build.sh
```

需要 Wails CLI 时，在同一云端容器执行 `bash scripts/cloud-linux.sh run go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`，再执行 `bash scripts/cloud-linux.sh run /tools/gopath/bin/wails3 version`。实测输出为 `v3.1.0`，与 Go 模块和前端 runtime 一致。常规构建直接使用固定的 Go 模块。

前端检查通过锁文件安装依赖，校验语言资源并做 TypeScript 检查和生产构建。Go 检查验证模块、运行 vet 和实际测试。最终归档位于 `dist/`，包含平台、架构与 Git commit。含未提交改动的本地构建另加 `-dirty` 后缀。先构建前端，再编译 Go，确保 WebView 资源嵌入可执行文件。

使用云端预装的 Python Playwright 和 Chromium 运行 `python3 tests/ui/preview.py`，可复验浏览器预览中的双语导航、语言包导入、错误提示和回退。

在同一云机上，可从打包产物启动实际 Linux 桌面程序并自动操作窗口。测试使用独立的临时用户数据，依次打开页面、保存连接、切换语言，结束进程后重新启动，并核对保存结果。

```bash
stage=$(mktemp -d)
revision=$(git rev-parse HEAD)
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then revision="$revision-dirty"; fi
tar -xzf "dist/s3s-linux-amd64-$revision.tar.gz" -C "$stage"
bash tests/ui/linux-container.sh "$stage/S3S"
```

连接元数据及任务状态保存在系统的用户配置目录下的 `S3S/workspace.sqlite3`。SQLite 保存连接名称、类型、区域和端点。凭据单独存入所选连接对应的系统凭据库条目。更改服务商、区域或端点后，为新配置设置凭据。界面中的语言选择、导入的语言包保存在本机 WebView 用户数据中。

## 工程入口

| 路径 | 用途 |
| --- | --- |
| `main.go`、`workspace.go` | 桌面窗口、资源嵌入、Go 与界面的元数据接口 |
| `internal/core/` | 独立于 GUI 的连接、状态、传输选项、能力与结构化错误 |
| `internal/s3client/`、`internal/vault/` | AWS/R2 协议请求及连接隔离的系统凭据管理 |
| `internal/transfer/` | 条件范围下载、暂停与恢复、临时文件及最终发布 |
| `internal/store/` | SQLite 迁移、状态转换和分片检查点 |
| `frontend/src/` | Vue 工作区、语言包解析与独立翻译资源 |
| `scripts/`、`.github/workflows/` | 云端复现脚本、原生构建与开发产物 |

产品范围见 [产品规格](docs/PRODUCT_SPEC.md)。开发前请查阅 [开发约定](AGENTS.md)、[架构与固定版本](docs/ARCHITECTURE.md)、[路线图](docs/ROADMAP.md) 和 [当前状态](docs/STATUS.md)。跨平台产物由 GitHub Actions 构建，平台检查和产物说明见 [测试](docs/TESTING.md) 与 [发行流程](docs/RELEASING.md)。
