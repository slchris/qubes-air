# kix-discipline spec（需求三检契约）

- 记录时间: 2026-09-22T07:00:50.238Z

## Goal（要解决的根本问题）
在 p2-nongo 分区内闭合 Sprint 1 的 T4（文档漂移 D-1~D-7）、T5（门禁脚本盲区 EP-2/EP-5）、T3（2 个 0 测试前端组件，含取消/拒绝负向路径），不越界、不削弱门禁。

## XY 检查（需求三检①：要 X 真需要的是 Y？）
用户要 X=「按 plan 跑完 T3/T4/T5」，真需要 Y=「把已核实的文档漂移与门禁盲区真正闭合，且闭合方式本身可被机械门禁验证」——所以每个改动都必须留下 文件:行号 证据与可复跑的门禁输出，而不是"看起来改好了"。

## 前提假设（需求三检②：前提可验证吗？）
1) 代码是唯一真相源，文档冲突以代码为准（runtime-context §7.1）。2) `make docs-check` 只能通过 Makefile:138-140 调用的两个 .mjs 生效，Makefile 禁止修改 → EP-5 检查必须落在既有脚本内。3) `console/backend/**` 属 p1-go 分区，不能改 .go（含 _test.go），否则跨分区冲突 → EP-5 走脚本方案。4) 本机可跑 npm/go/node，不可跑 yamllint/pwsh/真机。

## 更优路径（需求三检③：有更高维度解法吗？）
顺序执行：T4（文档为主，先闭合 D-1~D-7）→ T5（脚本护栏 + 存量 workflow 处置 + EP-5 脚本检查）→ T3（前端组件测试）。EP-5 用「在 check-doc-links.mjs 内加引用存在性检查」而非改 .go 测试，因为 Makefile 不可改 + console/backend 属另一分区；D-7 新建 docs/runtime-defaults.md 集中承载默认值，避免把运维默认值塞进安全/可靠性文档造成主题混乱。

## 验收标准（可验证的完成定义）
T4：D-1~D-5 每条给三态+证据；D-6/D-7 落 docs/runtime-defaults.md 且每个默认值有 文件:行号；`git status --short` 证明无 .go/.svelte/.ts 改动；`make docs-check` exit 0。T5：临时违规 workflow 能被新检测判失败且已删除；dependency.yml 两处 || true 处置有据；EP-5 存在性检查可复现失败/通过；`make docs-check` exit 0 且 `cd console/backend && go test ./...` 全绿。T3：`npm run test` 用例数 >20 全绿、`npm run check` 0 error 0 warning、`npm run build` 通过。

## 执行模式（编曲留痕：成员组合 + 一句理由）
solo：分区由编排器指定为单执行者（T4/T5 共享 docs-check 门禁，拆分会产生隐性耦合），本单不派子代理，全部改动手工完成并自跑 gate。

## 行为契约（必须不变 / 必须改变 / 必须成立 / 歧义解读）
必须不变：`make docs-check` 的 CI 通过/失败语义、`console/backend` 任一 .go 文件内容、`docs/sprint-1/{plan,progress,drift-check,runtime-context}.md`、Makefile、.golangci.yml。必须改变：docs 中与代码矛盾的措辞（D-1/D-2/D-3）、check-workflow-gates.mjs 对 `|| true` 的检测、check-doc-links.mjs 增加测试引用存在性检查、dependency.yml 去掉 `|| true`。必须成立：新增前端测试用例数从 20 增加且全绿；docs-check 与 go test 全绿。契约歧义与解读假设：① 任务说"D-7 的 14 项默认值"——drift-check §5 的 UD-1~UD-14 共 14 条（每条含 1~4 个取值），我按 14 条登记并逐条给取值+行号；② `go-licenses` 那条本机无法验证是否通过（需联网 go install + 完整依赖解析），按任务要求保留为非阻塞但去掉 `|| true` 改为显式 continue-on-error，并记开放问题。
