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
	@echo "  tf-plan        Terraform plan"
	@echo "  tf-apply       Terraform apply"

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

# Terraform
tf-init:
	cd terraform && terraform init

tf-plan:
	cd terraform && terraform plan -var-file=environments/dev.tfvars

tf-apply:
	cd terraform && terraform apply -var-file=environments/dev.tfvars

# Salt
salt-apply:
	@echo "Applying Salt states (run in dom0)..."
	sudo qubesctl --all state.apply

# 密钥生成
keys:
	cd crypto/scripts && chmod +x generate-keys.sh && ./generate-keys.sh
