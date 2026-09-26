# MCP-01：Xpra 单次截图客户端与经批准的取帧路径

原始记录：2026-09-23（归档线 `archive/local-main-2026-09-25`，当时只有截图客户端、没有调用方）。
2026-09-26 随 `port/g1-xpra-library` 与 `port/g2-desktop-frame` 重新核对，正文按这两个分支的实际实现改写；
归档线上的门禁数字不适用于本仓库，已删去。

## 实现

- `console/backend/internal/xpra.GetScreenshot`：只发一个带 `request=screenshot` 的 hello。服务端（按
  上游 v6.2–v6.4 与 master 源码）不发服务端 hello，直接回一个图像包；包名 `screenshot` 与
  `display-screenshot` 都接受。只读一个 record：challenge、ssl-upgrade、disconnect、服务端 hello
  （表示请求未被处理）、0×0 空图或其他包都失败关闭。原记录写的“接受服务端 hello 后的
  `display-screenshot`”与上游行为不符，已由 G1 更正。
- 上限：单个 record 4 MiB、PNG 2 MiB、单边 8192、总像素 4 Mi、会话 10 s；校验 PNG 头部尺寸与包里
  声明的一致后才完整解码。context 取消或超时关闭 stream，且只关闭一次。
- 调用方（G2）：
  - `internal/desktopaccess`：只签发 frame 的 consent 状态机，一次性 grant 绑定 subject + qube +
    operation，审批窗口与 grant 各 30 s，进程内保存；
  - Console API：`POST /qubes/:id/desktop-access`、`POST /qubes/:id/desktop-frame`、
    `GET /desktop-access` 与 approve/deny/stop，批准只接受浏览器 session；
  - `service.CaptureDesktopFrame`：每次取帧签发 2 分钟的 `console-desktop` 客户端证书，对端钉在
    `agent-<qube>`，经该 qube 自己的 agent 走 `qubesair.StreamTCP+10005`；agent 只对 Relay 与
    `console-desktop` 开放这个端口；
  - MCP 工具 `desktop_frame_get` 返回 image 内容块；Console 的 Desktop access 页提供 Allow / Deny / Stop。
- 不处理连续帧、LZ4、raw chunk 或 Xpra 输入包。`desktop_input_send` 仍显式失败，Console 拒绝签发
  input grant。

判定规则、各端点的认证与上限见[安全控制](../security-controls.md#mcp-桌面帧授权)与
[运行期默认值](../runtime-defaults.md) UD-26 系列。

## 自动化验收（2026-09-26，本仓库）

在 `port/g2-desktop-frame` 的工作树上运行（基线 `integration/todo-batch-2` `c5e9604`）：

- `make BASE_REV=c5e9604 pre-commit`：通过。Go `-race` 全量测试、总覆盖率 70.7%（门槛 61%）、入口冒烟、
  增量与全模块 golangci-lint（darwin 与 `GOOS=linux`）0 issues、govulncheck 无漏洞、前端 check/build
  （0 error、0 warning）与 14 个测试文件 112 个测试、qrexec 服务测试 194 项、yamllint、文档链接检查。
- `make audit`：通过。全仓 gosec 扫描 138 个文件、34,139 行，0 issues；其余完整门禁同上。
- 每个提交单独 `go build`、`go vet`、`go test` 通过（`git rebase --exec`）。
- 负向覆盖包括：Bearer token、只读 scope、zone-scoped 凭据、鉴权关闭、缺少 action 头与明文远端连接
  都拿不到或批不了 grant；grant 换 qube 或换请求方即 403 并作废；`input` 400；无桌面传输 503；
  并发满 503 且不消耗 grant；Stop 与到期中断进行中的取帧；审计行与响应里没有 grant。

## 部署互通边界

核对权威 Salt 仓库 qubes-salt-config（独立仓库，与本仓库并列检出；以下路径相对它的根目录），
2026-09-26 复查结论不变：

- `salt/mgmt/remotevm/files/qubesair.ConnectTCP` 与本仓库 Relay/agent 已支持原始双向流及
  `10000–10010` GUI 端口 allowlist；`salt/qubesair/create.sls` 的 `ui_port` 是 Console UI 通道，
  不能代表 Xpra listener 已部署。
- 两个仓库里都没有 `qubes-air-xpra.service` 的 unit，也没有创建 Xpra TCP listener 的部署状态。
  本仓库 `remote/qubes-rpc/qubes.StartApp` 只引用该 service 与 display `:100`，不能证明它已安装运行。
  没有监听时，Console 取帧以 502（桌面不可达）失败关闭。
- `qubesair.ConnectTCP` 以 `relay.crt/key`（Relay 角色）调用 `relay-call`。本仓库新增的
  `console-desktop` 身份只用于 Console 直连 agent 取帧；agent 对 10005 端口的身份限制不影响 Relay
  路径，升级顺序见[升级与回滚](../upgrade-rollback.md) §2.6。
- 本仓库 Proxmox 模板脚本支持 Debian 12 和 Debian 13。Debian Bookworm 官方 source package 为
  Xpra `3.1.3-0.1`（GPLv2+）；Xpra 官方 stable 仓库同时提供 Bookworm 与
  Trixie suite，Xpra 项目说明 Linux packages 均有签名，是两种模板的候选来源。仓库 keyring/fingerprint、
  依赖安装与真实互通都未验证。参考：[Debian Bookworm Xpra](https://packages.debian.org/source/bookworm/xpra)、
  [Xpra stable Trixie 仓库](https://xpra.org/stable/trixie/)、[Xpra 上游安装说明](https://github.com/Xpra-org/xpra#installation)。
- Xpra 上游手册指出 TCP bind 未配置认证属于重大安全风险，即使监听只在 loopback 也一样。当前客户端
  遇到 challenge 失败关闭（Console 返回 502），不支持认证协商；部署 listener 时需要一种与短期身份
  绑定、客户端能完成的认证方式。参考：[Xpra manual](https://xpra.org/manual)。
- 原记录列出的接线前提里，“固定端口、专用短期 identity 与 agent 侧身份校验、Console 用该 identity
  建立 stream 并消费单次 frame grant”已在本仓库代码中实现；“受限的 Xpra listener 与它的认证”仍需在
  部署配置里完成。

2026-09-23 曾尝试在 Debian Bookworm 容器安装 `xpra xvfb xterm` 做真实 wire 互通验证，但容器内 apt
HTTP 下载持续无响应，容器已清理，没有得出协议通过或失败的结论。

## 结论

自动化测试证明的是 Console 侧整条取帧路径的行为：consent 状态机、API 的认证/授权/上限、真实 mTLS
下的 `console-desktop` 身份与 agent 端口限制、按上游源码构造的假 Xpra 服务端上的截图交换。它不证明与
目标 qube 上真实 Xpra 服务端的互通（客户端依赖 6.3 及以上把 PNG 内联进包里），也不证明 listener 已部署。
`MCP-01` 因此记为部分完成：帧路径已接线，输入仍是显式失败的桩，真实互通与部署未验证。
