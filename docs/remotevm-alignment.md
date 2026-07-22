# 对齐 Qubes OS RemoteVM

本项目使用 Qubes OS R4.3 的 `RemoteVM` 原语表达远端身份和 qrexec transport。

## RemoteVM 提供什么

RemoteVM 是 dom0 中的元数据 qube，关键属性是：

| 属性 | 含义 | 当前值示例 |
|---|---|---|
| `relayvm` | 承载 transport handler 的本地 AppVM | `mgmt-jump` 或专用 Relay |
| `transport_rpc` | dom0 改写后调用的 Relay 服务 | `qubesair.GrpcProxy` |
| `remote_name` | 远端 agent 的逻辑名称 | `remote-dev-1` |

它没有本地运行时，生命周期方法会失败。因此不要对 RemoteVM 做 start/shutdown；控制台的
suspend/resume 操作云端 compute 实例。

## 调用改写

本地 AppVM 发起：

```bash
qrexec-client-vm <remotevm> <service>+<argument>
```

dom0 先按原始 caller、RemoteVM target 和 service 评估 policy，然后把请求改写为 Relay 上的
transport RPC。`qubesair.GrpcProxy` 保留 target、service 和 argument，经 mTLS 调远端 agent。

## 本项目增加的部分

RemoteVM 定义了“怎样委派”，但不会替普通 Linux 远端实现 qrexec。本项目补充：

- remote agent 与 allow-listed services；
- 独立 Relay 的 CSR 身份和 gRPC handler；
- console 端 endpoint 发布、证书签发和健康探测；
- Proxmox 置备、bootstrap 与存算分离；
- dom0 policy 和部署 state（由 qubes-salt-config 管理）。

## 已验证边界

已验证：RemoteVM 自动注册、`GrpcProxy` 改写、独立 Relay、`Ping`/`Exec`/`FileCopy` 和 mTLS
TCP streaming。

正在完成：将 `GetAppmenus`、`StartApp` 与 Xpra 原语组合成稳定的无缝桌面体验。

不应宣称与本地 Qube 完全等价：

- RemoteVM 没有本地 block device、Xen state 或 Qubes GUI agent；
- 剪贴板、文件复制和 GUI 都需要显式远端服务，不会凭 RemoteVM 元数据自动获得；
- 云端生命周期由 console/provider 管理，不由 `qvm-start` 管理；
- 普通 Linux 远端没有第二个 dom0 policy。

## Transport

`qubesair.GrpcProxy` 是 RemoteVM transport：独立 Relay 使用 mTLS 连接 agent，已经过真机验证。

Qubes 侧配置以
[qubes-salt-config](https://github.com/slchris/qubes-salt-config) 为准。
