# S3S

S3S 是用 Go、Wails 3、Vue 3 和 SQLite 构建的对象存储桌面客户端。当前为 M0 工程版本。可在桌面工作区保存连接的公开配置、查看对象与任务页面、切换简体中文和英语，以及导入语言包。云端鉴权、桶和对象请求、文件传输与断点续传在后续里程碑接入。开发构建无需云账号。

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

前端检查通过锁文件安装依赖，校验语言资源并做 TypeScript 检查和生产构建。Go 检查验证模块、运行 vet 和实际测试。最终归档位于 `dist/`，包含平台、架构与 Git commit。先构建前端，再编译 Go，确保 WebView 资源嵌入可执行文件。

使用云端预装的 Python Playwright 和 Chromium 运行 `python3 tests/ui/preview.py`，可复验浏览器预览中的双语导航、语言包导入、错误提示和回退。

在同一云机上，可从打包产物启动实际 Linux 桌面程序并自动操作窗口。测试使用独立的临时用户数据，依次打开页面、保存连接、切换语言，结束进程后重新启动，并核对保存结果。

```bash
stage=$(mktemp -d)
tar -xzf "dist/s3s-linux-amd64-$(git rev-parse HEAD).tar.gz" -C "$stage"
bash tests/ui/linux-container.sh "$stage/S3S"
```

连接元数据及任务状态保存在系统的用户配置目录下的 `S3S/workspace.sqlite3`。M0 只收集连接的名称、类型、区域和端点。界面中的语言选择、导入的语言包保存在本机 WebView 用户数据中。

## 工程入口

| 路径 | 用途 |
| --- | --- |
| `main.go`、`workspace.go` | 桌面窗口、资源嵌入、Go 与界面的元数据接口 |
| `internal/core/` | 独立于 GUI 的连接、状态、传输选项、能力与结构化错误 |
| `internal/store/` | SQLite 迁移、状态转换和分片检查点 |
| `frontend/src/` | Vue 工作区、语言包解析与独立翻译资源 |
| `scripts/`、`.github/workflows/` | 云端复现脚本、原生构建与开发产物 |

产品范围见 [产品规格](docs/PRODUCT_SPEC.md)。开发前请查阅 [开发约定](AGENTS.md)、[架构与固定版本](docs/ARCHITECTURE.md)、[路线图](docs/ROADMAP.md) 和 [当前状态](docs/STATUS.md)。跨平台产物由 GitHub Actions 构建，平台检查和产物说明见 [测试](docs/TESTING.md) 与 [发行流程](docs/RELEASING.md)。
