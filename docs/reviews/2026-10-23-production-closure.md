# 2026-10-23 生产闭环修复

基线：Qubes Air `6b40f52`，部署仓库 `qubes-salt-config` `464ef0f`。
本阶段按可重复部署、离机恢复、Exec/FileCopy 完整 qrexec 路径的顺序推进。

## 行为、边界与验收

1. 部署：Console、Web、agent 和备份程序必须绑定可核对的构建与 SHA256；候选 Console
   在替换运行程序前只读检查数据库 schema。不兼容或无法读取时拒绝部署，不迁移数据库。
   Salt 再次应用不得回退到旧制品，首次和重复应用后核对构建身份与健康状态。
2. 恢复：备份只能写入已挂载的离机介质。归档与恢复口令、原 keyring 分别保管；验证到
   独立路径，核对凭据解密、CA、吊销记录与 provider 资源，并记录实际恢复耗时。
   不覆盖活动数据库；介质丢失、口令错误、目标已存在必须失败。
3. qrexec：仅为明确的调用方、测试 RemoteVM 和 Exec/FileCopy 服务授权；guest 仍执行
   程序及目录 allowlist。通过完整 dom0 → Relay → agent 路径验证成功与拒绝；不能以
   guest 内直接执行脚本替代。purge 前单独核对并确认测试资源身份。

代码检查通过只计为代码完成；现场步骤必须另附结果。Qubes states 只修改权威部署仓库。

## 只读预检

- Console 为 `e23265a`、schema 6、服务 active；无 queued/running job。
- 存在 error、released 历史对象，本轮不将其当作测试资源或自行销毁。
- Console 没有备份程序和 `backup.env`，离机目录未挂载。
- 已确认用户指定的 Qubes 管理入口与离机存储主机可以登录。
- 数据库启动路径在迁移之后才拒绝过新 schema；新增回归测试要求拒绝时不新增表。

## 执行状态

### 数据库部署前检查（代码完成）

- 先用回归测试复现：未来 schema 的数据库原有 1 张表，启动拒绝前已增加到 15 张。
- 启动检查已前移到 WAL 模式变更及迁移之前，失败关闭连接；测试验证文件字节和原表不变。
- 新增 `--check-database PATH`：只读读取已有库与活动 WAL，不加载 Console 配置；覆盖
  旧/当前/未来 schema、文件缺失/损坏、目录、特殊文件名、空参数、取消和超时。
- `make pre-commit` 与 `make audit` 通过；Go race 总语句覆盖率 74.5%，前端检查无错误/警告。

部署仓库修复与三个现场闭环继续进行；尚未声明新的真机验收通过。
