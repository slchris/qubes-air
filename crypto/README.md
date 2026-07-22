# Crypto utilities

这里的脚本只管理仍使用 SOPS/age 的离线材料。现行 Relay/agent 身份和 console credential
加密不由这些脚本管理。

## 脚本

| 脚本 | 用途 |
|---|---|
| `scripts/generate-keys.sh` | 在 `KEY_DIR`（默认 `~/.qubes-air/keys`）生成 age key |
| `scripts/encrypt-secrets.sh` | 用 SOPS/age 加解密文件 |
| `scripts/rotate-keys.sh age` | 生成新 age key，并原子重加密已发现的 SOPS 文件 |

```bash
bash scripts/generate-keys.sh
bash scripts/encrypt-secrets.sh encrypt <plain.yaml>
bash scripts/encrypt-secrets.sh decrypt <encrypted.yaml>
bash scripts/rotate-keys.sh age
DRY_RUN=1 bash scripts/rotate-keys.sh age
```

私钥不进 Git、pillar 明文或云端；只有 age public recipient 可以提交。

Console AES-GCM key 使用 `console/backend/cmd/rotate-key` 轮换；数据盘 master、agent 和 Relay
证书也各有独立生命周期，不能用本目录脚本替代。
