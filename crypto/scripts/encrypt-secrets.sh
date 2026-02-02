#!/bin/bash
# Qubes Air - Secrets 加密脚本
#
# 使用 SOPS 加密敏感配置文件

set -euo pipefail

AGE_PUB_FILE="${AGE_PUB_FILE:-$HOME/.qubes-air/keys/age.pub}"
SOPS_CONFIG="$(dirname "$0")/../sops/.sops.yaml"

if [ ! -f "$AGE_PUB_FILE" ]; then
    echo "Error: age public key not found at $AGE_PUB_FILE"
    echo "Run generate-keys.sh first"
    exit 1
fi

AGE_PUB=$(cat "$AGE_PUB_FILE")

encrypt_file() {
    local input="$1"
    local output="${2:-${input}.enc}"
    
    echo "Encrypting: $input -> $output"
    sops --encrypt --age "$AGE_PUB" "$input" > "$output"
}

decrypt_file() {
    local input="$1"
    local output="${2:-${input%.enc}}"
    
    echo "Decrypting: $input -> $output"
    sops --decrypt "$input" > "$output"
}

case "${1:-}" in
    encrypt)
        shift
        encrypt_file "$@"
        ;;
    decrypt)
        shift
        decrypt_file "$@"
        ;;
    *)
        echo "Usage: $0 <encrypt|decrypt> <file> [output]"
        echo ""
        echo "Examples:"
        echo "  $0 encrypt secrets.yaml"
        echo "  $0 decrypt secrets.yaml.enc"
        exit 1
        ;;
esac
