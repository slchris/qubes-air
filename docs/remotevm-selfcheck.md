# RemoteVM 自检清单

按层检查；前一层未通过时不要跳到后一层改 policy。

## A. Console 与置备

- [ ] provision job 成功，无 Terraform/OpenTofu 错误；
- [ ] Qube 有当前 IP/endpoint；
- [ ] `agent_health=healthy`；
- [ ] agent artifact URL 可达且 SHA256 与配置一致；
- [ ] cloud-init 不含 agent 私钥、Relay 私钥或 CA 私钥。

## B. Agent

- [ ] `qubes-air-agent` unit active；
- [ ] agent 只监听预期的 mTLS endpoint；
- [ ] 身份文件权限正确；
- [ ] allowlist 非空且只含需要的服务；
- [ ] 证书有效期和当前时间正常；
- [ ] LUKS Qube 的数据盘已挂载到预期位置。

## C. dom0 RemoteVM

```bash
qvm-prefs <remotevm> relayvm
qvm-prefs <remotevm> transport_rpc
qvm-prefs <remotevm> remote_name
qvm-tags <remotevm>
```

- [ ] `transport_rpc=qubesair.GrpcProxy`；
- [ ] `relayvm` 指向现行 Relay；
- [ ] `remote_name` 与 console/agent 一致；
- [ ] 存在 `remote-zone` tag；
- [ ] 没有尝试启动 RemoteVM。

## D. Relay

- [ ] CSR bootstrap/renew timer 成功；
- [ ] Relay 私钥只在 Relay，权限正确；
- [ ] `/remote-endpoint/<name>` 存在且地址当前；
- [ ] `qubesair.GrpcProxy` 和 `relay-call` 已部署；
- [ ] handler 运行用户能读证书，不能读 console CA 私钥；

## E. Policy

- [ ] 指定 caller 可对目标 RemoteVM 调 `Ping`；
- [ ] `Exec`、`FileCopy`、GUI/TCP 使用 `ask` 或更窄规则；
- [ ] Relay 不能直达 dom0 admin API；
- [ ] 未使用非法服务名通配；
- [ ] 拒绝操作时远端没有副作用。

## F. 端到端

```bash
qrexec-client-vm <remotevm> qubesair.Ping
printf 'id\n' | qrexec-client-vm <remotevm> qubesair.Exec
```

- [ ] Ping 返回正确远端身份；
- [ ] Exec 经批准后返回远端用户信息；
- [ ] FileCopy push/pull 的字节数与 SHA256 一致；
- [ ] ConnectTCP/GUI 不要求直接开放远端应用端口。

## G. Suspend / resume

- [ ] suspend 删除 compute、保留 data disk；
- [ ] resume 挂回同一 data disk；
- [ ] agent 重新 healthy；
- [ ] Relay endpoint 自动刷新；
- [ ] RemoteVM 不需手工重建；
- [ ] 加密盘数据仍可用，日志未泄露密钥。

## H. 最小暴露

- [ ] 云防火墙只开放 bootstrap/agent 实际需要且有来源限制的路径；
- [ ] GUI/VNC/RDP 端口不直接暴露给 LAN/公网；
- [ ] console API 不使用开发 token，CORS 非 `*`；
- [ ] state backend 使用客户端加密；
- [ ] Git 中没有凭据、私钥或生成的真实 tfvars/state。
