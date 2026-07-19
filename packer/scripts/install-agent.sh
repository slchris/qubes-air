#!/bin/bash
# Qubes Air Agent 安装脚本
#
# 在 Packer 构建过程中执行

set -euo pipefail

echo ">>> Installing Qubes Air Agent..."

# 创建目录结构
sudo mkdir -p /opt/qubes-air/{bin,etc,log}

# 下载 Agent (占位符 - 实际应从构建系统获取)
# sudo curl -fsSL https://releases.qubes-air.example.com/agent/latest/qubes-air-agent \
#   -o /opt/qubes-air/bin/qubes-air-agent
# sudo chmod +x /opt/qubes-air/bin/qubes-air-agent

# 安装 agent 二进制
#
# 曾经这里写的是一个 `exec sleep infinity` 的占位脚本 —— 它让 systemd 单元看起来
# 是 active 的, 但什么也不做。真实 agent 现在在 console/backend/cmd/qubes-air-agent,
# 交叉编译后由构建流程放到这里:
#   GOOS=linux GOARCH=amd64 go build -ldflags "-X main.buildVersion=$(git describe --tags --always)" \
#     -o qubes-air-agent ./cmd/qubes-air-agent
if [ -f /tmp/qubes-air-agent ]; then
    sudo install -m 0755 /tmp/qubes-air-agent /opt/qubes-air/bin/qubes-air-agent
else
    echo ">>> ERROR: /tmp/qubes-air-agent not staged; the image would ship without an agent" >&2
    exit 1
fi

# qrexec 服务实现 (Qubes 约定路径)
sudo mkdir -p /etc/qubes-rpc
if [ -f /tmp/qubes-rpc/qubesair.Ping ]; then
    sudo install -m 0755 /tmp/qubes-rpc/qubesair.Ping /etc/qubes-rpc/qubesair.Ping
fi

# 创建 systemd 服务
sudo tee /etc/systemd/system/qubes-air-agent.service > /dev/null << 'EOF'
[Unit]
Description=Qubes Air Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# mTLS 材料由 cloud-init 注入。三者缺一 agent 会拒绝启动 —— 明文运行意味着
# 局域网上任何人都能执行本机的 qrexec 服务。
ExecStart=/opt/qubes-air/bin/qubes-air-agent \
    --listen ${QUBESAIR_LISTEN} \
    --remote-name ${QUBESAIR_REMOTE_NAME} \
    --ca /etc/qubes-air/ca.pem \
    --cert /etc/qubes-air/agent.pem \
    --key /etc/qubes-air/agent-key.pem
EnvironmentFile=/etc/qubes-air/agent.env
Restart=always
RestartSec=5

# 安全配置
User=root
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/qubes-air/log
# 需要读证书
ReadOnlyPaths=/etc/qubes-air /etc/qubes-rpc

[Install]
WantedBy=multi-user.target
EOF

# 启用服务
sudo systemctl daemon-reload
sudo systemctl enable qubes-air-agent

echo ">>> Qubes Air Agent installed successfully"
