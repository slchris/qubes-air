# 2026-10-23 Console 重启后的入站规则

新专用对象的首次 bootstrap 暴露了部署缺口：Console AppVM 重启会丢弃临时 nftables
入站规则。Console 的 loopback health、监听端口和 guest agent 均正常，但 agent 无法
获取签名吊销清单，拒绝 bootstrap 身份，最终 provision 超时。本次保留这条失败记录。

## 修复范围

权威实现位于 [Salt PR #6](https://github.com/slchris/qubes-salt-config/pull/6)，
部署源码为 `eacdc4cf42164c64193ee05849d2d58cad4f24c7`，已合入 `77a9185`，
合并提交的 CI `39945635093` 完成且通过。用户已明确授权以下范围：

- `allow_gateway_ingress` 默认关闭；现场启用后，Console 的 TCP 8080 只接受其 Qubes
  上游网关，经既有 LAN 18080 转发访问。相同端口的 Web/API 均可达；API 认证和签名
  吊销验证继续执行。上游 SNAT 后，Console 无法区分原客户端，来源限制由上游负责。
- 固定路径的 root helper 校验 Qube 名、IPv4 listener、非特权端口和 QubesDB gateway，
  只处理 `ip qubes custom-input` 中自己的标记规则。持久 flock、单次 nft transaction
  和读回校验使并发、重复调用及部分失败可见；不修改其他规则和上游 NAT。
- 配置、helper 和锁拒绝不可信所有权、权限及符号链接。接口不接受参数、命令或写入路径。
  `allow_gateway_ingress=False` 只撤销本 helper 管理的规则。
- 同一 helper 接入 `/rw/config/rc.local` 和 firewall reload hook。普通 Console AppVM
  不启动 `qubes-firewall.service`，仅配置后者不能覆盖开机；两个入口共用同一把锁。
- 全量 Console apply 在 candidate 数据库预检通过后才更新规则，不因 ingress 变更重启服务。

## 失败记录与验证

先补回归再修复了三项真实契约差异：Salt `cmd.run` 重复传递 `python_shell`、目标 nft
版本的两种等价 ct state JSON 表达、以及普通 AppVM 不运行 firewall service。
第一次重启验证确实失败，补齐 `rc.local` 后才重新验收，没有把 Salt 成功当作重启成功。

- 本地 54 个部署/运行时测试通过；全量 salt-lint、严格 YAML、100 对 state/top、
  两个渲染后启动钩子的 ShellCheck 和 diff 检查通过。
- 最新源码的 GitHub workflow `39943785750` 完成且 conclusion 为 success。
- 真实 Console apply 为 39 成功、0 失败。重启前核对全局无活动 job，指定测试对象 suspended。
- 通过 dom0 正常关闭并启动 Console，两个命令均 exit 0。boot ID 从
  `b70bfb1c-e05d-406f-aec2-5e6acac01777` 变为 `3c19152f-ff66-4217-9d0c-c92d9362d64f`。
- 未重新 apply 或手工添加规则，开机后即验证只有一条精确 gateway/8080 规则；NAS 经既有
  转发读取吊销清单为 HTTP 200、2693 bytes，响应含 payload/signature。
- Console health 为 healthy，源码 `06cfff3781d43f02783436f5d521b3029c5030f5`、clean tree；
  离机备份目录确为挂载点，backup timer active。
- 重启后再次 apply 仍为 39 成功、0 失败，Console PID 保持 740，单条规则及外部访问保持正常。

此记录只证明 Console 的入站持久性。Relay 整机重启、上游转发重建、独立带外 SSH
指纹核验和完整灾难接管仍需分别验收；用户已确认当前没有独立备用管理入口。
