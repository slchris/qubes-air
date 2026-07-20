#!/bin/bash
# Qubes Air - 密钥生成脚本
#
# 生成 WireGuard 和 age 加密密钥

set -euo pipefail

KEY_DIR="${KEY_DIR:-$HOME/.qubes-air/keys}"
mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

echo "=== Qubes Air Key Generation ==="

# WireGuard 密钥不在这里生成 —— 见 crypto/scripts/rotate-keys.sh 里那段说明。
# 简版: 私钥应当在**要用它的那台机器**上生成 (网关自己, 或持有 wg0 的那个 qube),
# 由 console 编排, 只有公钥出来。往 $KEY_DIR 写一份明文私钥是第三套凭据机制。

# age 加密密钥
echo "Generating age keys..."
if [ ! -f "$KEY_DIR/age.key" ]; then
    if command -v age-keygen &>/dev/null; then
        age-keygen -o "$KEY_DIR/age.key" 2>/dev/null
        chmod 600 "$KEY_DIR/age.key"
        grep "public key" "$KEY_DIR/age.key" | awk '{print $NF}' > "$KEY_DIR/age.pub"
        echo "  Private key: $KEY_DIR/age.key"
        echo "  Public key:  $KEY_DIR/age.pub"
    else
        echo "  WARN: age not installed, skipping"
    fi
else
    echo "  age keys already exist"
fi

echo ""
echo "=== Public Keys ==="
echo "age:       $(cat $KEY_DIR/age.pub 2>/dev/null || echo 'N/A')"
echo ""
echo "Add these public keys to your remote Zone configuration"
