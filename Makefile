# Qubes Air Makefile
#
# 常用构建和开发命令

.PHONY: help build clean dev test agent-deb publish-agent-deb release-agent \
	pre-commit audit check-tools diff-check test-race lint-new gosec-new gosec-ci-new \
	complexity-new vuln-check frontend-check shellcheck-new docs-check \
	frontend-audit-new frontend-audit lint-all gosec-all gosec-ci complexity-all shellcheck-all \
	agent-deb-test

# 默认目标
help:
	@echo "Qubes Air - Make Targets"
	@echo ""
	@echo "  build          Build all components"
	@echo "  build-backend  Build Go backend"
	@echo "  build-frontend Build Svelte frontend"
	@echo "  dev            Start development servers"
	@echo "  test           Run tests"
	@echo "  pre-commit     提交前增量门禁: test/race/lint/gosec/复杂度/前端/Shell/文档"
	@echo "  audit          里程碑完整审计: 对全部存量代码执行所有门禁"
	@echo "  clean          Clean build artifacts"
	@echo ""
	@echo "  agent-deb         构建 qubes-air-agent .deb (Docker 内交叉编译 amd64)"
	@echo "  agent-deb-test    旧包安装 + 当前包升级冒烟 (Docker, 需网络装依赖)"
	@echo "  publish-agent-deb 上传 .deb 到 artifact store, 回读校验, 打印 console 配置"
	@echo "  release-agent     agent-deb + publish-agent-deb 一条龙"
	@echo ""

# 构建
build: build-backend build-frontend

# ============================================================
# console 构建版本注入 (M2-10 / G-H8)
#
# 三个值经链接期 -X 写进 console/backend/internal/buildinfo，包内没有第二份
# 来源：未注入时 /health、--version 一律报 unknown，而不是一个看起来像版本的
# 常量（改造前是 "0.1.0"，每个构建都一样）。
#
# 变量名写错时 -X 是**静默**不生效的，所以 cmd/server/version_test.go 会用
# 同样的 -X 真构建一次二进制并执行它 —— 改名或包路径漂移会在那里变红。
#
# 取值放在 recipe 里的 shell 变量中，而不是 make 层的 $(shell ...)：后者把值原样
# 插进 recipe 文本，shell 解析那一行时照旧会执行值里的 $(...) 或反引号——tag 名
# 是仓库可控输入（本机验证：make 变量里的 v1.2.3$(touch /tmp/x) 在 recipe 里真的
# 被执行）。命令替换的结果先落进 shell 变量、再用 "$version" 展开则只是数据，不
# 会二次解析；release.yml 把版本经 env 传入是同一个理由。
# ============================================================
CONSOLE_VERSION_PKG ?= github.com/slchris/qubes-air/console/internal/buildinfo

# 三个值与 -ldflags 的拼装放在一起，build-backend 与 dev 共用一份。
# `git describe --dirty` 的 -dirty 后缀就是"工作树有未提交改动"的标记，二进制
# 里的 tree=dirty 由它解析出来；这里不再单独传一个布尔值，免得两处说法打架。
CONSOLE_STAMP = \
	version="$$(git describe --tags --always --dirty 2>/dev/null || echo unknown)"; \
	revision="$$(git rev-parse HEAD 2>/dev/null || echo unknown)"; \
	build_time="$$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	ldflags="-X $(CONSOLE_VERSION_PKG).version=$$version -X $(CONSOLE_VERSION_PKG).revision=$$revision -X $(CONSOLE_VERSION_PKG).buildTime=$$build_time"

build-backend:
	@echo "Building Go backend..."
	@cd console/backend && $(CONSOLE_STAMP) && go build -ldflags "$$ldflags" -o bin/qubes-air-console ./cmd/server

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
	@(cd console/backend && $(CONSOLE_STAMP) && go run -ldflags "$$ldflags" ./cmd/server) & \
	(cd console/frontend && npm run dev)

# 测试
test:
	@echo "Running tests..."
	cd console/backend && go test ./...

# ============================================================
# 开发质量门禁
#
# pre-commit 只拒绝 BASE_REV 之后新增的 lint/security/complexity 问题，避免当前阶段
# 被不相关的存量债务卡死；测试、依赖漏洞、前端和文档仍做全量检查。
# audit 用于里程碑/release，扫描全部存量代码，必须清零后才能发布。
# ============================================================

BASE_REV ?= HEAD
GOLANGCI_LINT ?= golangci-lint
SHELLCHECK ?= shellcheck
GOVULNCHECK ?= govulncheck

pre-commit: check-tools diff-check test-race lint-new gosec-new gosec-ci-new complexity-new \
	vuln-check frontend-check frontend-audit-new shellcheck-new docs-check

audit: check-tools diff-check test-race lint-all gosec-all gosec-ci complexity-all \
	vuln-check frontend-check frontend-audit shellcheck-all docs-check

check-tools:
	@for tool in git go node npm python3 $(GOLANGCI_LINT) $(SHELLCHECK) $(GOVULNCHECK); do \
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
	cd console/frontend && npm run test

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
	node scripts/check-workflow-gates.mjs

lint-all:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m

gosec-all:
	cd console/backend && $(GOLANGCI_LINT) run --timeout=5m --enable-only=gosec

# CI 跑的独立 gosec(版本与 .github/workflows/security.yml 钉的完全一致, 只有输出格式不同)。
# 它和上面内嵌在 golangci-lint 里的 gosec 是两个程序, 抑制语法也不一样: 独立版认 `#nosec`,
# golangci 版认 `//nolint` —— 只跑内嵌版正是本地与 CI 对 gosec 结论分叉的原因, 所以固定版本跑两次。
# -exclude-generated: internal/transport/relaypb/*.pb.go 由 protoc-gen-go 生成、不手工维护,
# 其中的 unsafe (G103) 是 protoc 的输出, 两个入口都排除。
gosec-ci:
	cd console/backend && go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -fmt text -exclude-generated ./...

# 增量版: 只扫本次改动的包, 好让 `make pre-commit` 与 CI 对 gosec 的结论也一致。
# 独立 gosec 没有 golangci-lint 的 --new-from-rev, 所以从 BASE_REV 自己算变更包;
# 没有变更包就跳过 —— 不退回全量扫描, 否则每次提交都要付全仓代价 (那是 `make audit` 的事)。
GOSEC_CI_PKGS = $(shell git diff --name-only $(BASE_REV) -- console/backend \
	| sed -n 's|^console/backend/\(.*\)/[^/]*\.go$$|./\1/...|p' | sort -u | tr '\n' ' ')

gosec-ci-new:
	@if [ -z "$(GOSEC_CI_PKGS)" ]; then echo "gosec-ci-new: 无变更 Go 包, 跳过"; else \
		echo "gosec-ci-new: $(GOSEC_CI_PKGS)"; \
		cd console/backend && go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -fmt text -exclude-generated -quiet $(GOSEC_CI_PKGS); \
	fi

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

# 安装/升级冒烟需要 Docker 与网络(容器内 apt 安装 python3 依赖), 因而不进 pre-commit;
# 在 agent 打包/发布相关改动前手动运行, CI 也跑同一脚本。
agent-deb-test:
	scripts/test-agent-deb.sh

publish-agent-deb:
	scripts/publish-agent-deb.sh $(DEB)

# 用 $(MAKE) 串行调用而不是写成依赖: 依赖在 make -j 下会并行, 而这两步
# 有严格先后 —— 并行的话 publish 会去发上一次构建留在 dist/ 里的旧包。
release-agent:
	$(MAKE) agent-deb VERSION=$(VERSION)
	$(MAKE) publish-agent-deb

# Qubes 侧 states 在 qubes-salt-config 仓库, 不由本 Makefile 驱动。
# salt-apply 目标已移除 —— `qubesctl --all` 对本仓库无 state 可应用。

# 密钥生成
keys:
	cd crypto/scripts && chmod +x generate-keys.sh && ./generate-keys.sh
