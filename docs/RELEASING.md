# 构建产物与发布

GitHub Actions 提供按提交区分的开发构建。所有目标分支的普通 PR，以及推送到 `main` 或 `feat/**` 的提交会运行验证与构建。版本标签和 GitHub Release 由后续受控发布流程创建。

## 下载与核对

1. 在 PR 的 Checks 或仓库 Actions 页面进入与目标提交对应的运行，记录该次工作流展示的 SHA。PR 工作流可能检出 GitHub 生成的合并测试提交；核对工作流实际检出的版本。
2. 检查四个原生 job 各自的验证与构建步骤。核对 job 内操作系统、runner 架构和产物名。构建目标如下。

| 平台 | runner 标签 | 目标架构 | 内容 |
| --- | --- | --- | --- |
| Windows | `windows-2025` | amd64 | ZIP 根目录只有 `S3S.exe` |
| Linux | `ubuntu-24.04` | amd64 | tar.gz 根目录为 `S3S` |
| macOS | `macos-26` | arm64 | tar.gz 根目录为 `S3S.app` |
| macOS | `macos-26-intel` | amd64 | tar.gz 根目录为 `S3S.app` |

3. 在该次运行的 Artifacts 区域下载 `s3s-<GOOS>-<GOARCH>-<完整 SHA>.zip`（Windows）或同名的 `.tar.gz`（Linux、macOS）。Windows 解压一次后双击 `S3S.exe`。Linux 与 macOS 解开 tar 时保留执行权限。原生 job 先检查可执行文件架构，再检查归档的根目录与内容。上传步骤对单个已打包文件设置 `archive: false`，保留上述文件名及字节。
4. 记录下载项、归档内部路径和二进制架构，并另行记录桌面启动与交互测试的系统版本。macOS 应用以 15.0 为部署目标，开发构建使用 ad-hoc 本地签名。Developer ID 签名与公证在 M4 执行。Linux 动态链接依赖、Windows WebView2 和 macOS 最低支持版本须在声明支持的实际目标上验收。

GitHub 托管 runner 清单及镜像变动见 [GitHub Docs](https://github.com/github/docs/blob/main/data/reusables/actions/supported-github-runners.md) 和 [runner-images](https://github.com/actions/runner-images)。

## 后续测试版本（M4）

- 版本负责人从通过检查的具体提交准备发行候选，核对 [ROADMAP.md](ROADMAP.md) 的阶段门槛、迁移兼容性、未实现能力和已知限制。使用语义版本及预发布标识，例如 `v0.1.0-beta.1`。
- 单独设计受保护的发布工作流。发布权限、签名证书、时间戳服务和公证凭据只在受保护环境中授权，设置人工批准。普通 PR 不接触这些资源。
- 分目标生成安装包，Windows 使用 Authenticode 并验证签名与时间戳；macOS 使用 Developer ID 签名、公证和装订并验证 Gatekeeper；Linux 记录包格式、动态依赖和对应的包签名策略。
- 在干净的测试系统验证安装、首次启动、数据目录权限、升级迁移、凭据保留和卸载的数据处理，并验证本版本声明的云端行为。平台最低版本以实际测试记录为准。
- 最终签名、打包完成后对每份最终归档生成 SHA-256，记录源码 SHA、构建工具、runner 与产物对应关系。发布说明列出版本范围、已知问题、升级注意事项和校验方法。
- 完成验收及人工批准后，再创建受保护的发布标签、上传签名产物和校验清单、发布测试版本。如需更正已发布产物，保留原版本记录并发布新版本。

开发构建只随 Actions 运行按保留策略保存。发行包、签名、公证、安装验收和版本发布各自需要可追溯的记录。
