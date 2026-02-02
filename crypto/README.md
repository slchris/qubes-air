# Qubes Air - Crypto Utilities
#
# 加密相关脚本和配置

.
├── scripts/
│   ├── generate-keys.sh      # 生成 WireGuard/GPG 密钥
│   ├── encrypt-secrets.sh    # 使用 SOPS/age 加密敏感数据
│   └── rotate-keys.sh        # 密钥轮换脚本
├── sops/
│   └── .sops.yaml            # SOPS 配置
└── README.md
