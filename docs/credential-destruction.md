# 凭据 / 密钥销毁流程 (阶段3)

> 目标: 明确"单 Zone 下线 / 整机应急 / 远端 VM 销毁"三种场景下, 凭据与密钥如何彻底失效。
> 核心判断: 云上的数据靠 **crypto-shredding** (丢密钥) 而非覆写 (云 SSD/快照上 shred 无效)。

## 销毁矩阵 (先看这张表)

| 场景 | 云 API key | relay SSH 私钥 | age/SOPS 私钥 | 控制台凭据记录 | 远端盘数据 |
|---|---|---|---|---|---|
| 单 Zone 下线 | 吊销该 Zone 的 token | 换发/删该 Zone 的 key | 保留 (仍解其它) | 删该 Zone 的 credential 行 | crypto-shred: 丢该盘 LUKS 密钥 |
| 整机应急 | 全部吊销 | 全部作废 | 销毁 vault 私钥 | 整库作废 | 远端盘已 LUKS, 本地密钥丢 = 不可解 |
| 远端 VM 销毁 | (不涉及) | (peer 侧撤 authorized_keys) | (不涉及) | 标记该 qube 已销毁 | crypto-shred: 丢该盘密钥 |

铁律 (评审确认): **远端盘从创建就 LUKS 加密, 密钥只在本地 vault, 销毁 = 丢密钥而非覆写。**
理由: 云 SSD 有磨损均衡 + 快照 + 底层复制, `shred`/`dd` 覆写不保证物理擦除; 唯一可靠的是
从一开始就加密、销毁时丢密钥, 让密文永久不可解 (crypto-shredding)。

---

## 场景 1: 单 Zone 下线

前提: 该 Zone 对应一个云 provider 凭据 + 一把 relay transport key + 若干远端盘。

```bash
# --- 1a. 吊销云 API key (在云 provider 侧, 从 mgmt-air 操作或控制台) ---
#   Proxmox: 删除该 API token (pveum user token remove ...)
#   GCP:     禁用/删除该 service account key
#   AWS:     IAM deactivate + delete access key
#   吊销后, 即便凭据明文泄露也失效。

# --- 1b. 从 vault-cloud 删该 Zone 的凭据文件 (在 vault-cloud 内) ---
shred -u ~/.qubes-air/credentials/proxmox-token 2>/dev/null || rm -f ~/.qubes-air/credentials/proxmox-token
#   vault-cloud 是本地 qube 磁盘, 但仍优先 rm (本地盘可加 LUKS; 关键是云侧已 1a 吊销)。
#   relay SSH 私钥: 从 vault ~/.ssh 删除, 并在远端 authorized_keys 撤销对应公钥。
rm -f ~/.ssh/relay_transport ~/.ssh/relay_transport.pub

# --- 1c. 删控制台里的 credential 记录 (在 mgmt-air, 走控制台 API) ---
#   控制台 DELETE /api/v1/credentials/{id}  (见 credential_handler)
#   或直接 SQL: DELETE FROM credentials WHERE id = '<id>';

# --- 1d. crypto-shred 远端盘 (丢该盘 LUKS 密钥) ---
bash dom0-scripts/decommission-zone.sh --zone <zone-name> --shred-luks-key
#   见下方脚本: 从本地 vault 删除该盘的 LUKS 密钥, 云上密文从此不可解。
#   随后可安全地在云侧删除该 VM/磁盘 (terraform destroy 对应 target)。
```

## 场景 2: 整机应急 (笔记本丢失/被扣/疑似入侵)

目标: 让所有凭据、所有远端数据在最短时间内不可用。假设你还能远程操作云控制台。

```bash
# --- 2a. 云侧: 吊销一切 (从任意可信设备登录云控制台, 不依赖本机) ---
#   吊销所有 API token / service account key / IAM key。
#   这是最重要一步: 只要云凭据全废, 攻击者拿到本机也无法操作云资源。

# --- 2b. 远端数据: 天然已 crypto-shred ---
#   远端盘从创建就 LUKS, 密钥只在本机 vault-cloud (无网络, 未上云)。
#   本机丢失 = 攻击者拿到的是密文 + 无密钥; 你只要不让密钥泄露即可 (见 2c)。

# --- 2c. 本机 vault 私钥: 若本机在你控制下, 主动销毁 ---
#   在 vault-cloud 内:
rm -f ~/.qubes-air/credentials/* ~/.ssh/relay_transport
#   age 私钥 (解 SOPS pillar 的根): 销毁它 = 所有 SOPS 加密的 secrets 永久不可解。
rm -f ~/.qubes-air/keys/age.key
#   若本机已启用整盘/qube LUKS, 关机即让本机磁盘也回到密文态。

# --- 2d. 控制台加密密钥: 作废 ---
#   控制台加密 SQLite 凭据元数据的 QUBES_AIR_ENCRYPTION_KEY(S) 若只在你记忆/密管中,
#   不再提供即让 credentials.encrypted_data 不可解 (元数据本就不含私钥, 见架构)。
```

> 若本机【不在】你控制下 (已丢失): 你能做的就是 2a (云侧吊销)。远端数据靠 2b 的 crypto-shredding
> 保护 —— 这正是"远端盘从创建就 LUKS + 密钥只在本地"设计的意义: 丢机不等于丢数据机密性。

## 场景 3: 远端 VM 销毁 (正常退役单个远端 qube/VM)

```bash
# --- 3a. crypto-shred 该 VM 的盘 (丢本地密钥) ---
bash dom0-scripts/decommission-zone.sh --vm <remote-vm-name> --shred-luks-key

# --- 3b. 云侧删除 VM/磁盘 ---
#   terraform destroy -target=... (阶段1 模块; 本阶段不改 terraform)
#   或云控制台删除。因 3a 已丢密钥, 即便云快照/备份残留也是不可解密文。

# --- 3c. 撤销该 VM 的入站信任 ---
#   远端 Remote-Relay 的 authorized_keys 撤掉对应 relay 公钥 (若一机一 key)。
#   删除本地 RemoteVM 元数据: qvm-remove <remote-vm-name> (dom0)。
```

---

## crypto-shredding 脚本钩子

`dom0-scripts/decommission-zone.sh` 提供销毁钩子。它【不】覆写云盘 (无效), 而是:
1. 从本地删除该 Zone/VM 的 LUKS 密钥材料 (密钥只在本地这个设计的兑现点);
2. 删除本地 vault 里该 Zone 的凭据文件;
3. 提示运维完成云侧吊销与 terraform destroy (脚本不直接碰云, 避免误删)。

```bash
# 预览要删什么 (不实际删)
DRY_RUN=1 bash dom0-scripts/decommission-zone.sh --zone pve-prod --shred-luks-key

# 实际执行
bash dom0-scripts/decommission-zone.sh --zone pve-prod --shred-luks-key
```

密钥存放约定 (与本项目 vault 一致): LUKS 密钥文件在
`~/.qubes-air/keys/luks/<zone-or-vm>.key` (权限 600)。销毁即 `shred -u` 删除该文件。
之后云上该盘的密文没有任何地方能再解 -> 达成 crypto-shredding。

---

## 待真机确认

- [D1] 远端盘 LUKS 的实际接入点: 阶段1 packer/terraform 是否已把远端盘设为 LUKS + keyfile,
      keyfile 是否就是 `~/.qubes-air/keys/luks/<name>.key`。若命名不同, 调 decommission-zone.sh 的 `LUKS_KEY_DIR`。
- [D2] 云侧吊销 API 的确切命令随 provider 而异 (Proxmox/GCP/AWS); 本文给方向, 具体命令按 provider 文档。
- [D3] `shred` 在 vault-cloud 本地文件系统上有效 (本地盘非云 SSD); 但若本机盘也是 SSD + 有 LUKS,
      真正的兜底仍是整机 LUKS + 关机, 而非依赖 shred 覆写。
```
