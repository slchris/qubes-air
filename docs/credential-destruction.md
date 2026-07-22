# 凭据与密钥销毁

云 SSD、快照和底层复制使覆写不可靠。远端数据的销毁原则是“从创建时就加密，退役时销毁
解密能力”，但必须先确认当前 LUKS key 来源。

## 重要更正

现行加密数据盘不是每台 VM 在 `~/.qubes-air/keys/luks/<name>.key` 保存独立 keyfile。
Console 在加密 credential store 中保存一个 `qubes-air-luks-master`，按 Qube ID 派生
passphrase，并只通过 agent mTLS 用于解锁。

因此仓库中的 `dom0-scripts/decommission-zone.sh --shred-luks-key` 针对旧 keyfile 布局，
不能单独完成现行单 Qube 的 crypto-shred。不要把它的成功退出当作数据已经不可恢复。

## 单个 Qube 退役

1. 停止新任务并记录 Qube ID、云资源、data disk、RemoteVM 和证书记录。
2. 如果需要保留数据，先做加密备份并验证恢复。
3. 删除/销毁云端 compute 和 data disk，包括已知快照与备份策略。
4. 删除 dom0 RemoteVM 元数据和 Relay endpoint 缓存。
5. 删除 console 中 Qube/agent 记录。

现行 master 派生模型下，删除一个 Qube 记录并不会让旧盘密文在密码学上不可恢复：只要 master
和原 Qube ID 仍存在，key 可以再次派生。真正的“每 Qube 可独立 crypto-shred”需要引入
per-Qube 随机 secret 或可撤销的派生 salt，并完成迁移；这是尚未实现的安全改进。

## Zone 退役

1. 在 provider 侧先吊销该 Zone 的 API token/service-account key。
2. 从 console 删除 credential 记录，清除离线 vault 中的副本。
3. 逐一处理 Zone 内 Qube 的数据保留与云资源删除。
4. 删除 Zone、Infrastructure、RemoteVM 和对应 policy/tag。
5. 如果 Relay 只服务于该 Zone，撤销其证书并删除 Relay identity；共享 Relay 不要误删。
6. 检查远程 state 和备份中是否仍含资源元数据。

Provider 侧吊销比本地删除 credential 更优先：本地副本泄露后也应已经失效。

## 控制台或整机事件

当 Qubes 主机丢失或 console 可能被攻破：

1. 从另一台可信设备吊销全部 provider/backend 凭据；
2. 停止或隔离 console，撤销 Relay/agent 信任；
3. 轮换 API token、credential encryption key 和可安全轮换的 provider token；
4. 评估 console CA 是否泄露；若泄露，按 CA 灾难恢复处理整个 fleet；
5. 评估 `qubes-air-luks-master` 是否泄露：泄露意味着攻击者拿到远端盘副本和 Qube ID 后可
   派生所有数据盘 key；
6. 从可信备份恢复后重新签发身份，不复用可疑主机上的私钥。

## 销毁高价值根密钥的影响

| 材料 | 销毁结果 |
|---|---|
| 某 provider token | 对应基础设施 API 访问失效 |
| Console encryption key | 仍由该版本加密的 credential 行不可恢复 |
| Console CA private key | 无法续期/签发；现有证书到期后 fleet 失联 |
| `qubes-air-luks-master` | 所有加密数据盘不可恢复 |
| State passphrase | 远程加密 state 不可恢复 |
| Relay private key | 该 Relay 数据面失效，可重新 bootstrap |
| Agent private key | 该 agent 身份失效，可通过受控重建恢复 |

执行前必须同时确认目标、备份、回滚窗口和依赖范围。根密钥销毁是不可逆操作，不应由一个模糊
的 `--zone` 脚本自动完成。
