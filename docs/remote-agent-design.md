# Remote agent

`qubes-air-agent` 让普通 Linux VM 提供一组受限的 qrexec 风格服务。它不是
`qrexec-client-vm` 的移植，也不会把远端变成 Qubes dom0。

## 已实现

- guest 内生成私钥和 CSR；
- 单次 token bootstrap 与 mTLS 身份；
- 证书续期、健康探测和身份持久化；
- 显式 service allowlist，空 allowlist 拒绝启动；
- `Ping`、`Exec`、`FileCopy`、`ConnectTCP`；
- LUKS 数据盘初始化/解锁；
- Xpra/appmenu/StartApp 所需服务原语；
- systemd unit 与 Debian 包。

入口位于 `console/backend/cmd/qubes-air-agent`，核心实现位于
`console/backend/internal/agent`，远端脚本位于 `remote/`。

## 信任边界

远端 guest 和云宿主机都按可能被攻破处理。Agent allowlist、路径检查和沙箱能减少误用，
但不能替代本地 dom0 policy：攻击者控制 guest 后可以绕过 guest 内所有检查。

因此：

- 本地 caller 是否能调用某服务，由 dom0 policy 决定；
- agent 不持云 API 凭据、console CA 私钥或 Relay 私钥；
- 远端长期私钥必须在 guest 生成；
- 反向访问本地资源不能仅靠 agent 决定，必须回到 dom0 policy；
- 敏感数据在离开本地可信边界前加密。

## 服务执行

Agent 只接受启动参数中显式列出的服务。`Exec` 与文件操作使用独立 systemd scope，既能执行
必要命令，又不放松 agent 主进程的 unit 沙箱。`FileCopy` 对路径、大小、超时和原子写入做
约束；`ConnectTCP` 是 byte stream，不解释上层协议。

这些限制是纵深防御。高风险服务仍应在 dom0 使用 `ask` 或按 caller/target 精确授权。

## 身份生命周期

1. cloud-init 投递公开 CA、一次性 token 与 agent artifact digest；
2. agent 创建私钥/CSR，console 主动连接并签发证书；
3. 正常运行只使用 mTLS；
4. 证书到阈值后通过现有 mTLS 身份续期；
5. compute 重建时重新 bootstrap，不从数据盘复制传输私钥。

详见[Bootstrap 设计](bootstrap-design.md)。

## 非目标

- 模拟完整 qrexec/Xen vchan；
- 在远端做第二个权威 policy 引擎；
- 任意命令后门或默认开放所有服务；
- 在线迁移 VM 内存状态；
- 把 agent 当作云基础设施管理凭据的存储。
