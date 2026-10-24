# Transport 发布制品

Salt 已支持固定 `ConnectDesktop` 通道，但现有 release 不包含 `relay-call`，
也不包含同一部署使用的 `relay-bootstrap`、`issue-relay-cert`、`list-endpoints`。
这些程序此前没有构建身份查询，无法沿用 Console 的发布脚本校验版本与 revision。

本阶段验收条件：

- 四个 helper 提供无副作用的 `--version`，在读取 stdin、数据库、凭据或执行 qrexec 前退出。
- 复用现有 `buildinfo` 和构建脚本；未注入身份时明确报告 `unknown`。
- release 同批构建四个 Linux/amd64 helper，将它们纳入发布文件和 `SHA256SUMS`。
- 真实构建/执行测试覆盖注入与未注入身份，并验证查询不需要生产配置。
- 本地门禁与完整审计通过；PR 与合并提交 CI 通过后才准备版本发布。

信任边界仍是受信发布来源及部署端明确固定的 SHA256；版本文本不替代摘要校验。
本阶段不改变 qrexec 授权、证书权限或传输协议。制品发布、Salt pin 更新和生产 GUI
验收分别记录，不能用构建成功代替已部署。

## 实现与验收

四个入口在解析 `--version` 后立即打印共用的 `buildinfo`，不读取请求体、不打开
数据库、不查询 QubesDB，也不生成或签发证书。正常调用的输出与退出语义保持原样。
工作流用 `scripts/build-release-binary.sh` 构建并回读身份，将四个文件显式纳入
`SHA256SUMS` 和 release 上传清单。Salt 配置键见[升级说明](../upgrade-rollback.md)。

先红后绿的回归覆盖真实二进制：旧代码的四个入口均因未知 `--version` 失败，旧工作流
也缺少四项发布/摘要输入。修复后，未注入构建、带 dirty 标记的构建与发布脚本构建均通过；
查询时 stdin 保持打开、PATH 不含外部程序、无加密密钥且工作目录为空，确保版本查询
不会等待 RPC 输入或需要已部署状态。发布测试还检查精确 revision 和 RFC3339 构建时间。

2026-10-24 本地验证：

- 四个 helper 与 `internal/buildinfo` 测试通过。
- `make pre-commit audit` 通过；Go 覆盖率 75.6%，前端 271 项，qrexec 219 项，
  HOME/mount/session 分别 12/19/13 项；本机和 Linux lint、安全扫描、文档与 YAML 门禁通过。
- 在 Linux/amd64 Go 1.26.10 容器中实际构建并执行四个制品，逐项回读身份、
  `sha256sum -c` 及相同发布回归测试通过。验证构建明确使用 QA dirty 版本，不作为发布制品。

前置 Salt [PR #8](https://github.com/slchris/qubes-salt-config/pull/8) 已合入
`dcfbf3813a78aee235e847de6c3ad90e764844a1`，四项 PR 检查和合并后的 lint workflow 通过。
其代码接线与本阶段构建证据均不代表生产桌面已经部署或验收。
