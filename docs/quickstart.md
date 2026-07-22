# 快速入门

## 只运行控制台

```bash
git clone https://github.com/slchris/qubes-air.git
cd qubes-air
docker compose up
```

打开 <http://127.0.0.1:5173>，使用 token `devtoken`。本地栈关闭真实编排，不会操作云资源。
更多说明见[本地开发](local-dev.md)。

## 真机前置条件

- Qubes OS R4.3，支持 `RemoteVM`；
- 一台独立的控制台 AppVM、一台独立 Relay 和一个无网络 vault；
- 当前已验证的 provider 是 Proxmox；
- OpenTofu、可用的 Proxmox cloud-init 模板和局域网 artifact store；
- [qubes-salt-config](https://github.com/slchris/qubes-salt-config)，它是 Qubes 侧模板、
  systemd unit、qrexec 服务和 dom0 policy 的唯一来源。

## 部署顺序

1. 按 qubes-salt-config 的安装与 deployment checklist 配置 Qubes 环境。
2. 在 `salt/config.jinja` 中设置控制台、Relay、RemoteVM gRPC、agent artifact 和 Proxmox 参数。
3. 构建并发布 agent 包，记录脚本输出的 URL、SHA256 和版本：

   ```bash
   make release-agent VERSION=<version>
   ```

4. 部署或升级控制台。生产环境至少要设置 API token、32 字节加密密钥、受限 CORS，且不能
   复用 `docker-compose.yml` 的开发值。
5. 在 Web UI 中依次创建 Infrastructure、Credential、Zone 和 Qube。
6. 等待 provision job 完成，并确认 Qube 的 `agent_health` 变为 `healthy`。
7. 确认 dom0 已出现 RemoteVM，且 transport 使用 `qubesair.GrpcProxy`：

   ```bash
   qvm-ls --class RemoteVM
   qvm-prefs <remotevm> relayvm
   qvm-prefs <remotevm> transport_rpc
   qvm-prefs <remotevm> remote_name
   ```

## 验收

从被 policy 允许的本地 AppVM 执行：

```bash
qrexec-client-vm <remotevm> qubesair.Ping

printf 'uname -a; id\n' |
  qrexec-client-vm <remotevm> qubesair.Exec
```

`Ping` 应返回 `pong`；`Exec` 应返回远端命令输出。执行与文件操作默认可能触发 dom0 的
`ask`，这是安全策略，不是故障。

逐层排错用[自检清单](remotevm-selfcheck.md)，完整操作见
[RemoteVM runbook](runbook-remotevm.md)。

## 下一步

- [了解架构与信任边界](architecture.md)
- [配置凭据和密钥轮换](credential-vault.md)
- [配置加密 state backend](terraform-state.md)
- [查看尚未完成的工作](roadmap-to-production.md)
