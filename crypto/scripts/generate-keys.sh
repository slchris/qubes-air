#!/bin/bash
# Qubes Air - 密钥生成脚本
#
# 生成 WireGuard 和 age 加密密钥

set -euo pipefail

KEY_DIR="${KEY_DIR:-$HOME/.qubes-air/keys}"
mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

echo "=== Qubes Air Key Generation ==="

# WireGuard 密钥
echo "Generating WireGuard keys..."
if [ ! -f "$KEY_DIR/wg_private.key" ]; then
    wg genkey > "$KEY_DIR/wg_private.key"
    chmod 600 "$KEY_DIR/wg_private.key"
    cat "$KEY_DIR/wg_private.key" | wg pubkey > "$KEY_DIR/wg_public.key"
    echo "  Private key: $KEY_DIR/wg_private.key"
    echo "  Public key:  $KEY_DIR/wg_public.key"
else
    echo "  WireGuard keys already exist"
fi

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
echo "WireGuard: $(cat $KEY_DIR/wg_public.key 2>/dev/null || echo 'N/A')"
echo "age:       $(cat $KEY_DIR/age.pub 2>/dev/null || echo 'N/A')"
echo ""
echo "Add these public keys to your remote Zone configuration"
