# Qubes Air 快速入门指南

> **[DEPRECATED] 本文档描述的是早期 `sys-remote + WireGuard` 手工流程，已弃用。**
> 该方案被评审否决（把 relay 当网络网关、违反平面分离），相关脚本与 salt states 已删除，
> 本文引用的 `security.md` / `troubleshooting.md` 也不存在。当前的置备是控制台驱动的
> （建 zone → 建 qube → cloud-init 送 token → 控制台拨过去 bootstrap），
> 见 [bootstrap-design.md](bootstrap-design.md) 与 [getting-started.md](getting-started.md)。
> 下面内容仅作历史参考。


## 前置要求

- Qubes OS 4.2 或更高版本 (推荐 4.3)
- 至少一个远程基础设施 (Proxmox VE / GCP / AWS)
- 基本的 Terraform 和 Salt 知识

## 安装步骤

### 1. 克隆项目

在你的 Qubes 管理 Qube 中:

```bash
git clone https://github.com/slchris/qubes-air.git
cd qubes-air
```

### 2. 初始化 dom0 环境

将 dom0 脚本复制到 dom0 (需要手动操作):

```bash
# 在管理 Qube 中
qvm-copy-to-vm dom0 dom0-scripts/

# 在 dom0 中
sudo bash /home/user/QubesIncoming/管理Qube名称/init-qubes-air.sh
```

### 3. 配置远程 Zone

编辑 Terraform 配置:

```bash
cd terraform
cp environments/dev.tfvars my-env.tfvars
vim my-env.tfvars
```

### 4. 生成密钥

```bash
cd crypto/scripts
chmod +x generate-keys.sh
./generate-keys.sh
```

### 5. 启动 sys-remote

```bash
# 在 dom0 中
qvm-start sys-remote-pve
```

### 6. 部署基础设施

```bash
cd terraform
terraform init
terraform plan -var-file=my-env.tfvars
terraform apply -var-file=my-env.tfvars
```

## 下一步

- 阅读 [架构文档](architecture.md)
- 查看 [安全配置指南](security.md)
- 了解 [故障排除](troubleshooting.md)
