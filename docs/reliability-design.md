# 生命周期可靠性与恢复契约

更新：2026-09-20。本轮实现针对单个 Console worker、可信 SQLite 数据库及同一 Proxmox Zone。
自动化故障注入不替代真机回归；真实恢复、快照和历史密钥副本仍属 DATA-01/OPS-01/QA-01。

## Purge 的不可逆意图

名称确认后，数据库原子地把 Qube 置为 deleting、设置永久 `purge_requested`，并撤销其证书、
作废未兑换 bootstrap token。数据库触发器拒绝随后插入该 Qube 的证书/token，防止并发签发
在撤销后恢复身份。迁移使用 schema 2；不能把该数据库交给仅支持 schema 1 的旧程序。

随后解除数据盘保护、再次幂等撤销身份、删除当前数据密钥，最后入队删除 provider 资源。
部分失败返回错误，保留 purge 意图，状态回到 error；Start、Release 和编辑会被数据库拒绝，
只能再次确认原名后重试 purge。界面提供 Retry purge。请求取消后的失败状态写入有独立的
5 秒期限；写入本身失败也会报告，需要恢复数据库后重新启动进行对账。

| 中断位置 | 保留事实与恢复方式 |
|---|---|
| 原子 claim 失败 | 不执行后续副作用；修复数据库或等待在途操作结束 |
| 解除保护、撤销或删 key 失败 | 保留 purge_requested；修复错误后重新确认并重试 purge |
| 入队失败 | 已发生的撤销/删 key 不回滚；重试 purge，不能恢复计算实例 |
| compute 删除失败/仍存在 | 保留 VMID；重试停止与删除，不提前清空 ID |
| holder 删除失败 | 保留 holder/data volume 身份；重试原资源 |
| holder 缺失但数据盘仍在 | 保留 infra 并报告残留；核对该盘的所有权及引用后单独处置，再重试核验 |
| 资源所有权不符、节点变更或任务仍锁定 | 停止操作；人工对账或等待任务结束，不创建替代数据盘、不删除未知资源 |
| provider 核验失败 | 不删除 infra 行，不报告 purged；恢复查询能力后重试 |
| RemoteVM 或端点清理失败 | job failed、Qube error；provider 清理已完成也可重试，直到后续清理成功 |
| 最终结果写库失败 | 保留未完成 job 供启动对账；日志明确报告无法持久化，不伪造成功记录 |

provider 先确认 compute 已不存在，再清除 ComputeVMID。purge 分别检查 holder、数据盘和
记录的 cloud-init snippet；当前 snippet 在上传前记录，销毁时仅删除该精确文件。
资源均已消失才删除 infra 行，端点和 RemoteVM 清理成功后才写 purged。
历史替换过的 snippet、外部复制/快照及备份不属于当前 infra 清单，不能据此宣称它们都已删除。

存储列表必须成功返回且具备完整可见性。Proxmox 会过滤无权访问的卷，因此核验前后检查
该 datastore 的有效 `Datastore.Allocate` 权限，缺少权限或空/错误响应明确失败。
此行为依据官方 [Storage::check_volume_access](https://github.com/proxmox/pve-storage/blob/master/src/PVE/Storage.pm)
及 [Storage Content API](https://github.com/proxmox/pve-storage/blob/master/src/PVE/API2/Storage/Content.pm)。
权限或资源在检查后被外部管理员改变不在单个 worker 的原子保证内；部署凭据需有对应权限。

## 创建与重试

Proxmox 分配 VMID 后，必须先把 node、Qube ID、VMID 写入 qube_infra，再发送 create/clone。
没有 checkpoint 或保存失败就不发创建请求。请求超时、响应丢失或进程退出后，重试使用相同 ID。
创建请求自身写入由 Qube ID 派生的所有权标记；接管和删除前核验标记与 provider lock。
旧记录对应资源若没有可验证标记，会拒绝操作，需要人工核对，不能盲目信任 VMID。

已有 holder 的数据盘缺失/变更，或者已有数据盘对应 holder 消失时，不创建空盘冒充恢复。
已克隆但未配置完成的 compute 会完成配置并启动，保留已有静态 IP；已完成的 compute 不重新克隆。
创建中断导致没有完整 storage 记录时，Start 会先恢复 Provision 步骤，再继续 compute。
本轮不自动迁移 node，不把更换 Zone/cluster endpoint 当作原资源的恢复方式。

## 重启和重复请求

启动不会自动重放 provider 变更：queued → failed，running → unknown，相关 Qube → error；
没有 job 的 pending/transient Qube 同样转 error。queued 的准备步骤可能已经执行；running 的 compute
状态只是诊断信息，不能用于推断整个操作成功。保留 job 历史、资源身份及 purge_requested，
由操作者核对后重试原动作。purge 意图存在时即使状态为 error 也不能 Start。

同一 Qube 的原子 claim 拒绝并发动作；已完成 purge 再次请求是无副作用的成功。
编辑不写回过期的生命周期状态/IP，也不能覆盖在途 claim。Runner 拒绝关闭后的提交，
开始状态写库失败时不调用 provider，完成清理失败时不记录 succeeded。
不提供跨多个 Console 进程的自动接管或 exactly-once 外部副作用承诺。

## 验收范围

回归覆盖误报 purge、未持久化就执行、丢失创建响应、持久化失败、错误所有权、残留资源、
权限过滤、并发 claim、实际数据库重开、purge 后身份重新签发、RemoteVM 清理失败及重复重试。
每阶段执行 `make pre-commit`；本轮涉及身份与销毁控制，还执行 `make audit`。
本轮结果见[验收记录](reviews/2026-09-20-lifecycle.md)，待办见 [TODO](TODO.md)。
