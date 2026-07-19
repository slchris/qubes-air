# Qubes Air Makefile
#
# 常用构建和开发命令

.PHONY: help build clean dev test

# 默认目标
help:
	@echo "Qubes Air - Make Targets"
	@echo ""
	@echo "  build          Build all components"
	@echo "  build-backend  Build Go backend"
	@echo "  build-frontend Build Svelte frontend"
	@echo "  dev            Start development servers"
	@echo "  test           Run tests"
	@echo "  clean          Clean build artifacts"
	@echo "  tf-init        Initialize Terraform"
	@echo "  tf-validate    Validate Terraform (init -backend=false + validate + fmt check)"
	@echo "  tf-plan        Terraform plan"
	@echo "  tf-apply       Terraform apply"
	@echo "  tf-suspend     存算分离: 销毁某 Qube 的计算实例、保留数据盘 (省钱) — 需 QUBE=<name>"
	@echo "  tf-resume      存算分离: 重建某 Qube 的计算实例、挂回同一数据盘   — 需 QUBE=<name>"

# 构建
build: build-backend build-frontend

build-backend:
	@echo "Building Go backend..."
	cd console/backend && go build -o bin/qubes-air-console ./cmd/server

build-frontend:
	@echo "Building Svelte frontend..."
	cd console/frontend && npm install && npm run build

# 开发
dev:
	@echo "Starting development servers..."
	@echo "Backend: http://localhost:8080"
	@echo "Frontend: http://localhost:5173"
	@(cd console/backend && go run ./cmd/server) & \
	(cd console/frontend && npm run dev)

# 测试
test:
	@echo "Running tests..."
	cd console/backend && go test ./...

# 清理
clean:
	rm -rf console/backend/bin/
	rm -rf console/frontend/dist/
	rm -rf console/frontend/node_modules/
	rm -rf packer/output/

# Terraform / OpenTofu
# TF_BIN 默认 tofu: 多机远程 backend 方案依赖 OpenTofu 的客户端 state 加密
# (HashiCorp Terraform 不支持)。仅本地 HCL 校验可回退 terraform: make TF_BIN=terraform tf-validate
TF_BIN ?= tofu

# 环境名 -> environments/<ENV>.tfvars。默认 dev 保持向后兼容。
# 例: make tf-plan ENV=infra   /   make tf-suspend QUBE=dev-work ENV=infra
ENV ?= dev
TFVARS ?= environments/$(ENV).tfvars

tf-init:
	cd terraform && $(TF_BIN) init

# 无需真实 provider/backend 即可校验 HCL: init 不连 backend + validate + fmt 检查。
# 用 -backend=false, 所以不需要 state 加密 passphrase, 可离线跑 (CI 友好)。
tf-validate:
	cd terraform && $(TF_BIN) init -backend=false -input=false && $(TF_BIN) validate && $(TF_BIN) fmt -check -recursive

tf-plan:
	cd terraform && $(TF_BIN) plan -var-file=$(TFVARS)

tf-apply:
	cd terraform && $(TF_BIN) apply -var-file=$(TFVARS)

# 走远程加密 backend 的安全入口 (在 mgmt-air 内): 从 vault-cloud 取 passphrase
# (pg backend 再取 PG_CONN_STR), 注入后再执行。
#   S3 兼容: make tf-secure ARGS="apply -var-file=environments/dev.tfvars"
#   pg     : make tf-secure BACKEND=pg ARGS="apply -var-file=environments/dev.tfvars"
tf-secure:
	@test -n "$(ARGS)" || (echo ' 用法: make tf-secure ARGS="apply -var-file=environments/dev.tfvars" [BACKEND=pg]'; exit 1)
	TF_BIN=$(TF_BIN) BACKEND=$(or $(BACKEND),s3) scripts/tf-with-passphrase.sh $(ARGS)

# ============================================================
# 存算分离 (compute/storage separation) — FinOps suspend/resume
#
# 语义:
#   suspend = 销毁计算实例 (省钱), 保留独立数据盘 (不丢数据)
#   resume  = 重建计算实例, 挂回同一数据盘
#
# 两种等价用法:
#   (A) 声明式 (推荐, 长期状态写进 tfvars):
#       在 environments/dev.tfvars 把该 Qube 的 compute_running 改为 false / true,
#       再 `make tf-apply`。这样 state 与意图一致, 团队可见。
#
#   (B) 命令式 (下面的 target, 单命令即时生效, 不改文件):
#       用 -target 只作用于该 Qube 的 compute 实例; storage-holder VM 与数据盘不受影响。
#       注意: 这是"临时"操作, 下次不带 -target 的 apply 会按 tfvars 里的
#       compute_running 值把状态拉回。要持久化请用 (A)。
#
# 用法: make tf-suspend QUBE=dev-work   /   make tf-resume QUBE=dev-work
# ============================================================

tf-suspend:
	@test -n "$(QUBE)" || (echo "用法: make tf-suspend QUBE=<qube-name>"; exit 1)
	@echo ">>> suspend '$(QUBE)': 销毁计算实例, 保留数据盘 (storage-holder VM 与 data 盘不动)"
	cd terraform && $(TF_BIN) destroy -var-file=$(TFVARS) \
		-target='module.remote_qubes["$(QUBE)"].module.proxmox[0].proxmox_virtual_environment_vm.compute'

tf-resume:
	@test -n "$(QUBE)" || (echo "用法: make tf-resume QUBE=<qube-name>"; exit 1)
	@echo ">>> resume '$(QUBE)': 重建计算实例, 挂回同一数据盘"
	cd terraform && $(TF_BIN) apply -var-file=$(TFVARS) \
		-target='module.remote_qubes["$(QUBE)"].module.proxmox[0].proxmox_virtual_environment_vm.compute'

# Salt
salt-apply:
	@echo "Applying Salt states (run in dom0)..."
	sudo qubesctl --all state.apply

# 密钥生成
keys:
	cd crypto/scripts && chmod +x generate-keys.sh && ./generate-keys.sh
