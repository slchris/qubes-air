# Qubes Air Makefile
#
# 常用构建和开发命令

.PHONY: help build clean dev test agent-deb publish-agent-deb release-agent \
	pre-commit audit check-tools diff-check test-race lint-new gosec-new \
	complexity-new vuln-check frontend-check shellcheck-new docs-check \
	frontend-audit-new frontend-audit lint-all gosec-all complexity-all shellcheck-all

# 默认目标
help:
	@echo "Qubes Air - Make Targets"
	@echo ""
	@echo "  build          Build all components"
	@echo "  build-backend  Build Go backend"
	@echo "  build-frontend Build Svelte frontend"
	@echo "  dev            Start development servers"
	@echo "  test           Run tests"
	@echo "  pre-commit     提交前增量门禁: test/race/lint/gosec/复杂度/前端/Shell/Terraform/文档"
	@echo "  audit          里程碑完整审计: 对全部存量代码执行所有门禁"
	@echo "  clean          Clean build artifacts"
	@echo ""
	@echo "  agent-deb         构建 qubes-air-agent .deb (Docker 内交叉编译 amd64)"
	@echo "  publish-agent-deb 上传 .deb 到 artifact store, 回读校验, 打印 console 配置"
	@echo "  release-agent     agent-deb + publish-agent-deb 一条龙"
	@echo ""
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

# agent-deb 的定义在下面的"Agent 分发"一节, 和 publish/release 放在一起。
# 它不进 build 目标 —— 需要 Docker, 而日常前后端开发不需要。

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

# ============================================================
# 开发质量门禁
#
# pre-commit 只拒绝 BASE_REV 之后新增的 lint/security/complexity 问题，避免当前阶段
# 被不相关的存量债务卡死；测试、依赖漏洞、前端、Terraform 和文档仍做全量检查。
# audit 用于里程碑/release，扫描全部存量代码，必须清零后才能发布。
# ============================================================

BASE_REV ?= HEAD
GOLANGCI_LINT ?= golangci-lint
SHELLCHECK ?= shellcheck
GOVULNCHECK ?= govulncheck

pre-commit: check-tools diff-check test-race lint-new gosec-new complexity-new \
	vuln-check frontend-check frontend-audit-new shellcheck-new docs-check tf-validate

audit: check-tools diff-check test-race lint-all gosec-all complexity-all \
	vuln-check frontend-check frontend-audit shellcheck-all docs-check tf-validate

check-tools:
	@for tool in git go node npm $(GOLANGCI_LINT) $(SHELLCHECK) $(GOVULNCHECK) $(TF_BIN); do \
		command -v "$$tool" >/dev/null 2>&1 || { echo "缺少开发门禁工具: $$tool" >&2; exit 1; }; \
	done

diff-check:
	git diff --check $(BASE_REV) --

test-race:
	cd console/backend && go test -race -coverprofile=coverage.out ./...

lint-new:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --new-from-rev=$(BASE_REV)

# 显式单独运行安全和复杂度 linter，防止以后修改默认 linter 集合时悄悄丢掉门禁。
gosec-new:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --new-from-rev=$(BASE_REV) --enable-only=gosec

complexity-new:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --new-from-rev=$(BASE_REV) --enable-only=gocyclo,funlen

vuln-check:
	cd console/backend && $(GOVULNCHECK) ./...

# Warning 也属于失败：保持为 0，不建立可永久继承的告警基线。
FRONTEND_WARNING_BUDGET ?= 0

frontend-check:
	cd console/frontend && npm ci
	@output="$$(cd console/frontend && npm run check 2>&1)"; status=$$?; \
	printf '%s\n' "$$output"; \
	[ $$status -eq 0 ] || exit $$status; \
	warnings="$$(printf '%s\n' "$$output" | sed -n 's/.*found 0 errors and \([0-9][0-9]*\) warnings.*/\1/p' | tail -n 1)"; \
	[ -n "$$warnings" ] || { echo "无法读取 Svelte warning 数量" >&2; exit 1; }; \
	[ "$$warnings" -le "$(FRONTEND_WARNING_BUDGET)" ] || { \
		echo "Svelte warning 增加: $$warnings > $(FRONTEND_WARNING_BUDGET)" >&2; exit 1; \
	}
	cd console/frontend && npm run build

# 依赖文件发生变化时，提交前必须检查 high/critical 漏洞；完整 audit 每次都检查。
frontend-audit-new:
	@if { git diff --name-only $(BASE_REV) --; git ls-files --others --exclude-standard; } | \
		grep -Eq '^console/frontend/(package.json|package-lock.json)$$'; then \
		cd console/frontend && npm audit --audit-level=high; \
	else \
		echo "npm audit: frontend dependencies unchanged"; \
	fi

frontend-audit:
	cd console/frontend && npm audit --audit-level=high

# 检查相对 BASE_REV 修改及新建的所有 shell/shebang 文件，包括无 .sh 后缀的 qrexec 服务。
shellcheck-new:
	@files="$$( \
		{ git diff --name-only --diff-filter=ACMR $(BASE_REV) --; git ls-files --others --exclude-standard; } | \
		sort -u | while IFS= read -r file; do \
			if [ -f "$$file" ] && head -n 1 "$$file" | grep -Eq '^\#\!.*/(ba)?sh'; then printf '%s\n' "$$file"; fi; \
		done \
	)"; \
	if [ -n "$$files" ]; then $(SHELLCHECK) $$files; else echo "ShellCheck: no changed shell files"; fi

docs-check:
	node scripts/check-doc-links.mjs

lint-all:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m

gosec-all:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --enable-only=gosec

complexity-all:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --enable-only=gocyclo,funlen

shellcheck-all:
	@files="$$(git grep -l -E '^\#\!.*/(ba)?sh')"; \
	if [ -n "$$files" ]; then $(SHELLCHECK) $$files; else echo "ShellCheck: no shell files"; fi

# 清理
clean:
	rm -rf console/backend/bin/
	rm -rf console/frontend/dist/
	rm -rf console/frontend/node_modules/
	rm -rf dist/

# ============================================================
# Agent 分发: 构建 .deb -> 传到局域网 artifact store -> 拿到 console 配置
#
# 为什么 agent 不烤进镜像 (2026-07 的决定, 详见 docs/bootstrap-design.md §6):
# 模板一旦固化 agent 版本, 每改一次 agent 就要重建镜像 + 重建所有 qube。
# 现在改成开机从 artifact store 装, 版本由 **console** 按 qube 钉死 (URL + SHA256)。
#
# 用法:
#   make agent-deb                     # 版本取自 git describe
#   make agent-deb VERSION=1.2.3       # 显式版本
#   make publish-agent-deb             # 发 dist/ 里那个 (多于一个时会让你指定)
#   make publish-agent-deb DEB=dist/qubes-air-agent_1.2.3_amd64.deb
#   make release-agent VERSION=1.2.3   # 两步连起来
#
# publish 打印的三行 (QUBES_AIR_AGENT_PACKAGE_URL / _SHA256 / _VERSION) 必须
# **原样**进 console 配置: artifact store 无认证且走明文 HTTP, 那个 SHA256 是
# 整条投递链路上唯一的完整性控制。
# 只有配置行走 stdout, 所以可以直接: make publish-agent-deb >> console.env
# ============================================================

# 留空则由各脚本自己决定 (build 用 git describe, publish 自动找 dist/)。
VERSION ?=
DEB ?=

# VERSION 必须显式写进 recipe 的环境: make 的命令行变量不会自动导出。
# 少了这个前缀, `make agent-deb VERSION=1.2.3` 会被脚本当成没设值, 悄悄回退到
# git describe —— 包名里的版本跟你要的不是一个, 而 console 钉的正是包名。
agent-deb:
	@echo "Building qubes-air-agent .deb (amd64)..."
	VERSION=$(VERSION) scripts/build-agent-deb.sh

publish-agent-deb:
	scripts/publish-agent-deb.sh $(DEB)

# 用 $(MAKE) 串行调用而不是写成依赖: 依赖在 make -j 下会并行, 而这两步
# 有严格先后 —— 并行的话 publish 会去发上一次构建留在 dist/ 里的旧包。
release-agent:
	$(MAKE) agent-deb VERSION=$(VERSION)
	$(MAKE) publish-agent-deb

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

# Qubes 侧 states 在 qubes-salt-config 仓库, 不由本 Makefile 驱动。
# salt-apply 目标已移除 —— `qubesctl --all` 对本仓库无 state 可应用。

# 密钥生成
keys:
	cd crypto/scripts && chmod +x generate-keys.sh && ./generate-keys.sh
