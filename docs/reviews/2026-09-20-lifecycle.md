# 2026-09-20 生命周期可靠性验收

范围：REL-01/REL-02；基于本地 main 与原有未提交工作区继续实现。未 commit、push、部署，
没有操作真实基础设施。当前恢复契约见[生命周期可靠性](../reliability-design.md)。

## 已复现并修复

- 重启时把 compute absent/suspended 推断为 purge 成功；现在未完成任务保持 unknown/error。
- job 的 running 状态写库失败仍执行 provider；现在拒绝执行。
- storage 创建响应失败时丢失拟创建 VMID；现在先 checkpoint，再发请求，重试原身份。
- purge 删 key 失败后还能 Start；现在持久化不可逆意图并阻止恢复计算实例。
- 过期的编辑请求覆盖 resuming 状态，释放并发保护；现在编辑不覆盖生命周期状态，且拒绝在途编辑。

原始失败测试均实际执行过。新增回归也覆盖丢失创建响应、错误所有权、checkpoint 失败、
独立资源残留、权限过滤、数据库关闭重开、撤销与再次签发、RemoteVM 清理失败及重复请求。

## 工程验证

本轮第一次 pre-commit 在新增 EnsureCompute 的复杂度检查处失败；拆成有明确职责的恢复与
创建步骤后通过，没有放宽阈值。首次 audit 又发现 Runner.run 的复杂度超限和尾部空行，
拆出任务完成处理函数并修复格式后重跑；最终结果以本记录下面的门禁结论为准。
临时原始日志保存在本机 `/tmp/qubes-air-rel-*.log`，不加入仓库；复现入口为仓库根目录
`make pre-commit` 和 `make audit`。

## 最终门禁结论

完整 `make pre-commit` 与 `make audit` 均通过：Go 全量 race/coverage、全量 lint/gosec/复杂度、
ShellCheck、前端零错误/警告检查与 build、Vitest 5/5、npm audit 零漏洞、本地文档链接及 CI 检查。
govulncheck 没有代码可达漏洞，仍提示 1 项当前不可达的依赖模块漏洞；不宣称所有依赖零漏洞。
测试检查当前工作区，尚无对应新 commit 或构建发布。REL-01/02 按代码与自动化验收勾选。
2026-09-21 补充：本轮改动已随工作区按独立意图分组提交到本地 `main`（未 push），每组
提交前重跑 `make pre-commit`；仍无真机验收或构建发布。

## 验收边界

恢复方式是保留身份和意图、显式重试原动作，不是自动重放队列，也没有跨 Console 进程接管。
未知所有权、丢失 holder 且有孤立磁盘、节点改变或权限不足会停止并要求人工对账，
不会通过创建替代盘或猜测销毁成功来自动“修复”。

当前记录以外的快照、历史 snippet、备份和密钥副本未自动清除。Proxmox 真机回归、安装升级、
离机恢复和 CA 演练仍待执行；本轮通过自动化检查不代表完成 QA-01 或 OPS-01。
