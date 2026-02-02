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

# 创建占位 Agent 脚本
sudo tee /opt/qubes-air/bin/qubes-air-agent > /dev/null << 'EOF'
#!/bin/bash
# Qubes Air Agent - 占位符
# 实际 Agent 将在构建时注入

echo "Qubes Air Agent v0.1.0"
echo "Status: Running"

# 保持运行
exec sleep infinity
EOF
sudo chmod +x /opt/qubes-air/bin/qubes-air-agent

# 创建 systemd 服务
sudo tee /etc/systemd/system/qubes-air-agent.service > /dev/null << 'EOF'
[Unit]
Description=Qubes Air Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/qubes-air/bin/qubes-air-agent
Restart=always
RestartSec=5

# 安全配置
User=root
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/qubes-air/log

[Install]
WantedBy=multi-user.target
EOF

# 启用服务
sudo systemctl daemon-reload
sudo systemctl enable qubes-air-agent

echo ">>> Qubes Air Agent installed successfully"
