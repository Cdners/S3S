# AWS S3 / Cloudflare R2 厂商验收

## 已知对象只读验证

在专用测试桶中选择一个已有的小文件（1 字节至 1 MiB），先取得其已知 SHA-256。测试只向所选桶发出带前缀的对象列表、HEAD 和条件范围读取，逐字节核对长度和摘要。单次验收限时 45 秒。

从云端工作区的安全配置注入所选账号的测试凭据。使用仅可列出测试前缀和读取测试对象的权限。普通 CI 无需这些配置。

| 变量 | 内容 |
| --- | --- |
| `S3S_TEST_PROVIDER` | `aws` 或 `r2` |
| `S3S_TEST_REGION` | AWS 桶的区域；R2 使用 `auto` |
| `S3S_TEST_ENDPOINT` | AWS 留空；R2 填账号的 HTTPS S3 端点 |
| `S3S_TEST_BUCKET`、`S3S_TEST_KEY` | 测试桶和完整对象 Key |
| `S3S_TEST_SHA256` | 测试对象的 64 位十六进制 SHA-256 摘要 |
| `S3S_TEST_ACCESS_KEY_ID`、`S3S_TEST_SECRET_ACCESS_KEY` | 专用测试身份 |
| `S3S_TEST_SESSION_TOKEN` | 临时凭据需要的会话令牌 |

在仓库根目录的 Go 环境中执行以下命令，各厂商单独运行并记录结果。

```sh
go test -mod=readonly -tags livecloud -count=1 -v ./tests/live
```

云端的 `scripts/cloud-linux.sh setup` 安装了可用的 Go 工具链。如果安全配置注入宿主进程，可在宿主设置 `PATH=/workspace/.s3s-tools/go/bin:$PATH`、`GOPATH=/workspace/.s3s-tools/gopath`、`GOCACHE=/workspace/.s3s-tools/gocache` 执行上述命令。厂商验收需要测试端点的出站访问，以及能够用于 SigV4 本地签名的专用临时凭据。网页报告仅记录厂商、检查项目和结果。

## 真实分片写入与中止

使用专用桶、专用凭据和前缀 `s3s-acceptance/`。在同一云端 Go 环境设置上述提供方、桶、区域或端点和鉴权变量后，显式运行：

```sh
go test -mod=readonly -tags liveupload -count=1 -v ./tests/live
```

这项测试会在唯一 Key 下创建分片会话，发送一个 5 MiB 的随机分片，核对服务返回的分片编号、长度和 ETag，然后中止会话并确认服务报告会话已消失。网络上传限时 90 秒，清理另留 30 秒。它不需要既有对象或参考摘要。所用身份需具备该前缀的分片写入、列举分片和中止权限。若创建响应丢失或测试进程被强制终止，桶的过期分片生命周期规则可清理未完成的会话。
