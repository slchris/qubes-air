# Qubes Air - Crypto Utilities
#
# 本地 vault 的密钥生成、加密与轮换工具。
#
# 红线 (评审确立): 私钥永不进 git、永不进 pillar 明文、永不上云。这些脚本只在本机
# KEY_DIR (默认 ~/.qubes-air/keys) 操作, 只把【公钥】打印出来供分发。

```
.
├── scripts/
│   ├── generate-keys.sh      # 生成 WireGuard + age 密钥到 KEY_DIR
│   ├── encrypt-secrets.sh    # 用 SOPS/age 加解密敏感文件 (encrypt|decrypt)
│   └── rotate-keys.sh        # 轮换 age / WireGuard / relay SSH 密钥 (阶段3)
├── sops/
│   └── .sops.yaml            # SOPS 创建规则 (age 收件人 = 你的 age 公钥)
└── README.md
```

## 密钥清单与存放

| 密钥 | 生成 | 存放 | 谁用 |
|---|---|---|---|
| age/SOPS 私钥 | generate-keys.sh | KEY_DIR / vault-cloud | 解密 salt pillar secrets |
| WireGuard 私钥 | generate-keys.sh | KEY_DIR (私钥留本地) | sys-remote VPN |
| relay transport SSH 私钥 | rotate-keys.sh ssh / ssh-keygen | vault-cloud ~/.ssh (split-ssh) | sys-relay 经 agent 用, 拿不到私钥 |
| 控制台加密密钥 (AES-256) | 运维自选 32 字节 | mgmt-air 控制台 config | 控制台加密 SQLite 凭据元数据 |

注意: **控制台加密密钥的轮换不在这里**, 它在 Go 控制台侧, 用
`console/backend/cmd/rotate-key` (多版本密钥, 向后兼容重加密)。见
`docs/credential-vault.md` 的"控制台密钥轮换"。

## 用法

### 生成初始密钥
```
bash scripts/generate-keys.sh
# 输出 WireGuard/age 公钥, 把公钥填进 sops/.sops.yaml 与远端 Zone 配置。
```

### 加解密 SOPS 文件
```
bash scripts/encrypt-secrets.sh encrypt salt/pillar/secrets.sls
bash scripts/encrypt-secrets.sh decrypt salt/pillar/secrets.sls.enc
```

### 轮换密钥 (rotate-keys.sh)
```
# 轮换 age 私钥并用新公钥重加密所有 SOPS 文件 (旧密钥先解后重加, 原子逐文件替换)
bash scripts/rotate-keys.sh age

# 轮换 WireGuard 私钥, 打印新公钥供分发 (私钥不外传)
bash scripts/rotate-keys.sh wg

# 轮换 relay transport SSH 私钥, 打印新公钥供追加到远端 authorized_keys
bash scripts/rotate-keys.sh ssh

# 三者一起
bash scripts/rotate-keys.sh all

# 先看会做什么, 不改动
DRY_RUN=1 bash scripts/rotate-keys.sh all
```

轮换脚本特性:
- **有备份**: 每次先把旧密钥/旧 SOPS 文件复制到 `KEY_DIR/backup/<时间戳>/`, 不静默覆盖。
- **幂等友好**: 用临时文件 (`.new`) 生成, 校验成功后原子 `mv` 就位。
- **失败不留半成品**: `set -euo pipefail`; SOPS 重加密任一文件失败即中止, 其余文件保持旧密钥可解,
  旧 age 私钥备份仍在 -> 可回滚。
- **只出公钥**: WireGuard/SSH 只打印新公钥, 私钥留本机。

轮换后需人工完成的分发 (脚本会提示):
- age: 更新 `sops/.sops.yaml` 的 age 收件人为新公钥并提交 (公钥可入 git); 确认新密钥可解后销毁旧备份。
- WireGuard: 把新公钥告知对端 peer; 更新经 SOPS 加密的 `secrets.sls` 里本机 private_key; 重应用 salt。
- SSH: 把新公钥追加到远端 Remote-Relay 的 authorized_keys; 私钥放 vault-cloud ~/.ssh 并 ssh-add;
  切换后重启 autossh 隧道。

## 与凭据下发 / 销毁的关系

- 凭据隔离下发 (vault-cloud + qrexec `qubesair.GetCredential` + split-ssh): 见 `docs/credential-vault.md`。
- 凭据/密钥销毁 (单 Zone 下线 / 整机应急 / 远端 crypto-shredding): 见 `docs/credential-destruction.md`。
