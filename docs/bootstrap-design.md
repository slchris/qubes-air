# Bootstrap 设计：从一个空集群到第一台带 agent 的 qube

> 状态：设计草案。促成它的是一次真机验证——链路本身跑通了，但 agent 二进制得**手工推**进 VM，
> 说明分发路径根本不存在。
> 关联：[remote-agent-design.md](remote-agent-design.md)、[credential-vault.md](credential-vault.md)、[getting-started.md](getting-started.md)

## 1. 问题

真机验证到最后一步卡住了：证书签发、cloud-init 投递、guest agent 起来、terraform 拿到 IP——
全通。但 `systemctl is-active qubes-air-agent` 返回 `inactive`，因为**模板里没有这个二进制**。

cloud-init 里那句 `systemctl enable --now qubes-air-agent` 一直在空转。它不报错（我们特意让它
软失败，否则会连累 guest agent），所以这个缺口在日志里几乎不可见。

把二进制手工推进去能让今天的验证收尾，但那不是分发方式——它只是证明了没有分发方式。

## 2. 一个新用户到底要做什么

现在的隐含前提，散落在各处而从未被集中说明：

| 前提 | 现状 | 应该由谁提供 |
|---|---|---|
| PVE endpoint | 手工填进 zone | **用户**（不可省） |
| PVE 凭证 | 手工建 credential | **用户**（不可省） |
| 节点 root SSH | 隐含要求，见 §4 | **用户**（不可省，但可收窄） |
| 数据存储启用 `snippets` | 手工改 storage.cfg | 系统探测 + 提示 |
| 模板 VM（**不含** agent，见 §6） | **不存在** | 系统生成 |
| agent `.deb` 已发布到 artifact store | 手工 `make release-agent` | 构建流程 |
| agent 包的 URL + SHA256 进 console | 手工填 | 发布脚本打印，见 §6.4 |
| `template_vm_id` / `template_node` | 手工填 | 镜像构建后自动回写 |
| agent CA | 首次用时自动创建 ✅ | 系统生成 |
| 进 qube 的 SSH 公钥 | 字段存在，无人填 | 系统生成，见 §5 |

只有前三行是**不可省的用户输入**。其余每一行现在都要人肉补，而且补错了才会在 apply 时才发现——
这次验证里，`template_node` 缺失表现为 Proxmox 的一句 `unable to find configuration file for VM 901`，
跟真正的原因隔了三层。

## 3. 顺序（依赖是真的，不是流程洁癖）

```
1. 凭证             →  存进加密凭证库
2. 探测集群         →  节点、存储(是否共享)、网桥、snippets 是否开
3. 开 snippets      →  没有它, 身份文件无处可放 (§4)
4. 构建镜像         →  debian-12 + qemu-guest-agent (**不含 agent**, §6)
5. 回写 zone        →  template_vm_id / template_node
6. 发布 agent .deb  →  make release-agent, 拿到 URL + SHA256 (§6.4)
7. 生成 SSH 密钥    →  私钥进凭证库, 公钥进 zone (§5)
8. 建第一台 qube    →  CA 此时自动创建
```

第 3 步必须在第 4 步前面：`snippets` 没开的话，PVE 连 volume 都解析不出来，哪怕文件已经躺在
磁盘上。第 7 步必须在第 8 步前面：公钥是 cloud-init 注入的，qube 建好之后再加就得重建。

第 6 步和第 4 步之间**没有**依赖——这正是 §6 那个决定买来的东西：发新版 agent 不用碰镜像。
但它必须在第 8 步前面，而且这条**不会**在 apply 时报错：包没发，qube 照样建得起来，
只是 agent 装不上。所以 console 应当在建 qube 前就检查这两个配置值是否存在。

## 4. 不舒服的那一条：console 需要节点 root SSH

**任意** cloud-init user-data 在 Proxmox 上只能走 *snippet*，而 PVE 的 API **没有 snippet 上传接口**
——只有 SFTP。所以 bpg provider 要 SSH 到节点上传文件。

即：**为了给一台 VM 递一张证书，console 需要每台 hypervisor 的 root。**

这个权限比它要办的事大得多，值得写下来而不是默默依赖。已知的几条路：

- **(a) 接受，但收窄。** 专用 SSH key，在 `authorized_keys` 里用 `command=` 限死只能跑
  接收 snippet 的那个脚本。权限还在 root，但能做的事被钉死了。
- **(b) 镜像里预置 bootstrap 凭证**，agent 开机自己来 console 换正式身份。
  用节点 root 换来一个**所有 qube 共享的密钥躺在镜像里**——更糟，且正是 mTLS 想消灭的东西。
- **(c) 只用 cloud-init drive 的原生字段**（`ciuser` / `sshkeys`）。写不了任意文件，
  投递不了 mTLS 材料。等于放弃 agent 身份。
- **(d) 每个节点上跑一个小的特权 helper**，只接受经认证的 snippet 上传。
  把「root SSH」收敛成一个窄接口。

**建议：先 (a)，之后 (d)。** (b) 和 (c) 是死路，写在这里是为了让后来者不用再想一遍。

## 5. SSH 密钥：两件事，别混

代码里 `ssh_public_keys` 这个字段容易让人以为 SSH 只有一种。实际有两种，用途和信任域都不同：

**节点 SSH**（console/terraform → PVE 节点）
: 基础设施凭证，root，用于 §4 的 snippet 上传。集群本来就有，不是我们生成的。

**Qube SSH**（运维 → qube 内部）
: **仅用于破窗调试**。日常控制面是 agent 的 mTLS。

这个区分是有后果的：**如果 SSH 变成进 qube 的常规路径，整套 mTLS 设计就白做了**——
控制面会悄悄从「可撤销的、按 qube 签发的证书」退回到「一把到处都能开的钥匙」。

所以 qube SSH 密钥应该：bootstrap 时**生成**（每个 zone 一对），私钥进加密凭证库，
公钥写进 `zone.ssh_public_keys` 由 cloud-init 注入。可轮换，可审计，且**不是某个人的私人密钥
被粘进配置文件**——后者一旦那个人离职或换机器，没人知道还有多少台机器信任它。

## 6. 镜像里放什么，不放什么

> **2026-07 改了主意。** 这一节原来写的是「agent 二进制烤进镜像」。现在**不烤了**——
> 改成开机时从局域网 artifact store 装 `.deb`。下面先说结论，再说这次换来了什么、
> 赔进去了什么。

**放：**
- debian-12 cloud image（对齐现网模板 `debian-12-cloudimg-template`，不是 packer 里那份
  从没跑过的 Fedora 39 —— 它的 `iso_checksum` 至今还是 `xxxxx`）
- `qemu-guest-agent`：没有它 terraform 等不到 IP，apply 会挂到超时。这次验证实测过。

**不放：**
- **任何 per-qube 的密钥或身份**。镜像是所有 qube 共享的；放进去的任何秘密都等于
  「一台被攻破 = 全部被攻破」。身份只经 cloud-init 到达，每台一份，可单独撤销。
- **agent 二进制本身**。见下。

### 6.1 为什么不烤进镜像

烤进镜像的代价是 §7 原来那条未决项：**agent 版本被模板 ID 锁死**。改一行 agent 代码，
就得重跑一次镜像构建，拿到一个新 `template_vm_id`，再回写 zone，再重建所有 qube ——
而重建一台 qube 的实际代价当时**从没量过**。等到量出来才发现太贵，那时模板已经生了一堆了。

现在的做法：镜像里什么 agent 都没有；cloud-init 从 artifact store 下载
`qubes-air-agent_<version>_amd64.deb`，校验 SHA256，`dpkg -i`。包里是：

```
/usr/bin/qubes-air-agent
/lib/systemd/system/qubes-air-agent.service
/etc/qubes-rpc/                      ← qrexec 服务实现
```

`/etc/qubes-air/agent.env` **不在包里**——它是 console 按 qube 渲染的
（`QUBESAIR_REMOTE_NAME` / `QUBESAIR_LISTEN`，见 `cloudinit.go`）。unit 用
`EnvironmentFile=` 读它。这个分界不是随手划的：包是所有 qube 共享的，env 是每台一份的，
跟「密钥不进镜像」是同一条线。

agent 二进制**不读任何环境变量，只认命令行 flag**。所以 unit 必须把 `${QUBESAIR_LISTEN}`
和 `${QUBESAIR_REMOTE_NAME}` 插进 `ExecStart`。`--ca` / `--cert` / `--key` 是**必填**，
缺一个 agent 就拒绝启动——这是故意的，明文跑意味着局域网上任何人都能执行本机的 qrexec 服务。

### 6.2 赔进去了什么

诚实记一下，这次交换不是白赚的：

- **镜像不再是不可变的**。开机会联网、会装东西。一台 qube 起来时长什么样，
  取决于那一刻 artifact store 上是什么——不再只取决于模板。
- **`template_vm_id` 不再决定 agent 版本**。同一个模板今天和下个月开出来的 qube，
  agent 可以是两个版本。想从模板 ID 反推「这台跑的什么 agent」，现在推不出来了。
- **多了一个开机期依赖**。artifact store 挂了、网不通，qube 照样起来，但 agent 装不上——
  又是一次「看起来正常、agent 是死的」。这正是当初真机验证踩的那个形状。
  所以 cloud-init 里下载+校验这一段**必须硬失败并留下明确日志**，不能沿用
  `systemctl enable --now qubes-air-agent || echo ...` 那种软失败。

### 6.3 找回来的那一半：版本由 console 钉

上面第二条能补回来大半：**console 为每台 qube 钉死一个确切的 version + SHA256**，
写进身份文件，并记进 job / DB。对应的 console 配置就是发布脚本打印的那三行：

```
QUBES_AIR_AGENT_PACKAGE_URL=http://10.31.0.2/local/qubes-air/qubes-air-agent_<ver>_amd64.deb
QUBES_AIR_AGENT_PACKAGE_SHA256=<64 位十六进制>
QUBES_AIR_AGENT_PACKAGE_VERSION=<ver>          # 说明性的, 以哈希为准
```

`VERSION` 不参与校验，但它是事后从 console 配置反查「这批 qube 跑的是哪次构建」的
**唯一**线索——模板 ID 已经不再承载这个信息了（§6.2 第二条）。

于是「哪台跑哪个版本」这个问题的答案，从**模板**搬到了**console**。而且搬过去之后其实
更细了——模板只能做到「这批 qube 同一个版本」，console 能做到**逐台**指定，灰度和回滚
都不用碰镜像。

搬不回来的是第一条和第三条：不可变性和开机期联网，是真的赔掉了。

### 6.4 新增的信任依赖：一个没有认证的明文 HTTP artifact store

这是这次改动里**最需要写下来**的部分，因为它很容易被当成实现细节混过去。

artifact store 现状（2026-07-19 实测，**写入侧比最初设计时严**）：

```
POST http://10.31.0.2/api/artifacts      ← 要登录 (username/password), 未登录 401
GET  http://10.31.0.2/api/artifacts      ← 同样 401
     http://10.31.0.2/local/<dir>/<file> ← 明文 HTTP, 无认证, 谁都能读
```

先纠正一处：写入**是有认证的**。设计初稿里写的「局域网上任何人都能覆盖任何 artifact」
**是错的**，写在这里而不是悄悄删掉，因为夸大的威胁模型和低估的一样有害——
它会让后来的人整体不信这份文档。

真实的残留风险是**传输**，不是写入：分发走明文 HTTP，没有 TLS，也没有服务端签名。
一台能做 ARP 欺骗或占据路径的机器，可以在字节到达 qube 的途中把包换掉——
不需要任何 store 凭证。如果 cloud-init 只是「下载然后 `dpkg -i`」，
那就是把每台 qube 的 root 交给任何能插进这条链路的人。

唯一挡住这件事的是 **SHA256 钉死**，而它成立的原因是**哈希走的是另一条路**：

```
console → terraform → SFTP → PVE snippet → cloud-init      ← 可信通道, 哈希走这里
artifact store → HTTP → qube                                ← 不可信通道, 字节走这里
```

哈希从可信通道来，字节从不可信通道来，qube 用前者验后者。所以下载通道不可信是
**可以接受**的——但前提是哈希**真的**被校验，且**校验失败必须中止安装**。
这里的 SHA256 不是「顺手加的完整性检查」，它是这条链路上**唯一**的完整性控制。
去掉它，整段就退化成「信任局域网上所有人」。

发布侧的对应措施在 `scripts/publish-agent-deb.sh`：上传后**重新下载再算一次哈希**，
对不上就拒绝打印配置。因为「我上传了什么」和「它现在对外发什么」是两个不同的断言——
同名文件已存在而 `overwrite=false` 时，服务端留着旧文件，而你手里的哈希是新的。

**残留风险，直说：**

- 这套只保证「装上的字节 == 发布时那份字节」。**它不保证发布的那份是好的**——
  没有签名，没有可信发布者身份。持有 store 凭证的人仍然可以在**下一次发布之前**
  放一个坏包；真正的门槛在于谁能跑 `make release-agent` 并把哈希填进 console。
- **明文 HTTP 是当前最实际的攻击面。** 写入要凭证，但读取不要，且不加密不签名。
  在这条链路上，哈希钉死挡的主要就是这个——不是「有人偷了 store 密码」。
- 明文 HTTP 意味着**内容不保密**。agent 二进制本来就不是秘密，所以这条可以接受；
  但这条路径上**永远不能**再加任何敏感东西。
- store 本身没有审计。谁在什么时候覆盖了什么，查不出来。
- **正确的长期解法是给 artifact store 加认证 + HTTPS**，或者干脆改成签名包
  （store 只管分发，信任来自签名）。在那之前，SHA256 钉死是**唯一**的控制，
  不是纵深防御里的一层——它就是那一层。

### 6.5 老的 packer 安装路径已退役

`packer/scripts/install-agent.sh` 把二进制装到 `/opt/qubes-air/bin/`，并自己在
`/etc/systemd/system/` 写了一份 unit，跟 `.deb` 的路径**完全对不上**：

| | 旧 packer 路径 | 现在的 `.deb` |
|---|---|---|
| 二进制 | `/opt/qubes-air/bin/qubes-air-agent` | `/usr/bin/qubes-air-agent` |
| unit | `/etc/systemd/system/qubes-air-agent.service` | `/lib/systemd/system/qubes-air-agent.service` |

**两者不能并存**，而且失败方式很脏：`/etc/systemd/system` 的优先级**高于**
`/lib/systemd/system`，所以旧 unit 会**静默盖掉**包里那份，并继续指向一个 `.deb`
根本不安装的路径。`systemctl` 看着配置正确，实际 exec 不到二进制——
又是这次改造要消灭的那类静默失败。

所以 agent 的分发彻底不再经过 packer：镜像里只留 debian-12 + `qemu-guest-agent`。
`packer/templates/fedora.pkr.hcl` 里对应的 provisioner 已移除，原因就写在那一行的注释里。

## 7. 身份投递必须按内容钉，不能按路径

真机验证第二次踩坑，形状和第一次一模一样——**报成功，实际什么都没送到**。

`proxmox_virtual_environment_file` 的 `source_file` 默认只跟踪 `path`。同一路径下
内容改了，terraform **看不见**：plan 显示无变化，apply 报成功，节点上还是旧文件。
实测现场：console 已经渲染出带 agent 安装脚本的新身份文档（9162 bytes），
节点上的 snippet 却还是几小时前那份（3237 bytes）。cloud-init 照着旧文件跑完、
状态 `done`、证书也都在位——只是 agent 从头到尾没装。没有任何一层报错。

修法是让资源依赖内容：

```hcl
source_file {
  path     = var.agent_user_data_file
  checksum = filesha256(var.agent_user_data_file)
}
```

**这条比它看起来重要得多。** 证书轮换走的是同一条路径：重新签发的证书写到同一个
文件名，如果 terraform 按路径跟踪，新证书**永远到不了 qube**，而每次 apply 都告诉你
成功了。等到旧证书过期，qube 集体失联，而在那之前所有信号都是绿的。

### 代价：改身份 = 重建计算 VM

加上 checksum 之后，内容变化会让文件资源被替换；VM 引用它的 id，所以**计算 VM 也会被
重建**。这不是 bug，是这条投递链路的固有约束——cloud-init 只在开机时读一次，
想让新内容生效本来就得重启。

存算分离在这里付了钱：持久数据盘带 `prevent_destroy`，重建的只是计算实例。
但这意味着**轮换证书不是一次热更新，是一次重建**。轮换策略要按这个代价来定，
别按「改个文件」来定。

### 7.1 证书轮换：已实现，走 mTLS 通道

> **2026-07 更新。** 这一节原来的标题是「证书轮换目前等于全体重建」。
> 续期协议已经做出来并真机验证通过，所以结论变了。促成它的分析保留在下面，
> 因为它解释了为什么**首次签发**和**后续轮换**必须走不同的通道。

**为什么 cloud-init 不能做轮换**（这部分依然成立）：

- 文件资源的 `id` 是 `local:snippets/<name>.yaml`，**由文件名派生，与内容无关**。
  改身份会重建 VM，不是因为 id 变了，而是 terraform 对被替换资源的计算属性保守地
  标成 "known after apply"，而 `user_data_file_id` 在 VM 上是 ForceNew。
- 但**就算把重建绕过去也没用**：cloud-init 只在首次开机读一次 user-data。
  原地改 snippet 而不重启，新证书永远不会被读进去。

也就是说：**cloud-init 是首次开机的置备通道，不是配置管理通道**。
首次签发用它是**对的**——那时还没有可信通道，它正是打破这个先有鸡还是先有蛋的东西。
错的是拿它做**后续**轮换。

### 7.2 实现的形状

**console 发起，CSR 式，私钥永不过网。**

方向是 console 拨向 agent，因为连接方向本来如此（agent 监听、console 拨入），
而且到期时间记在 console 的注册表里。两次调用走已有的隧道：

```
console → qubesair.BeginRenewal    → agent 生成新密钥对, 只交出 CSR
console   (用自己的 CA 签 CSR, 登记新指纹)
console → qubesair.CompleteRenewal → agent 校验后原子写入, 热加载
```

私钥全程留在 agent 内存里，直到匹配的签名证书回来才落盘。这顺带修掉了一个现存弱点：
首次签发时 console 生成密钥并经 cloud-init 下发，而那份数据任何持有
`VM.Config.Cloudinit` 的人都能读。轮换之后的密钥没有这个问题。

**旧证书自然过期，不主动吊销。** 立刻吊销会掐断在途连接，并制造一个 agent 手里
没有可用证书的窗口。

### 7.3 两个不对称，都是同一条原则

实现里有两处看起来可以对称处理、实际必须不对称的地方。审查各推翻过一次：

**吊销未安装的证书。** agent 是**先安装再回复**的，所以最常见的失败（回复丢失、
隧道断开）描述的是一台**已经装好新证书**的 agent。在那里吊销就是永久砖化——
探测器和续期器都会拒绝被吊销的对端，而续期恰恰要走那条刚死掉的通道，只能重建。

反过来，不吊销只是留下一个孤儿行，让调度器以为这台刚续过——坏，但**可恢复**。
所以：**只在能证实 agent 仍持旧证书时才吊销**，说不清就不动。

真正的修法不在这两者之间选，而是让调度器**按观测到的使用**（`last_seen_at`）
而非**签发意图**（到期时间最远）来判断新鲜度。孤儿从未被握手见过，自然不参与。

**resume 时重发身份。** 挂起会销毁计算实例，所以挂满整个续期窗口的 qube 无人续期，
90 天后 resume 会拿着过期证书起不来。修法是 resume 时重发——代价为零，因为
`compute` 和 `agent_identity` 都由 `compute_running` 门控，resume 时两者都是
count 0→1，属于**创建**而非修改。

但守卫必须问对问题。原来的守卫用 `computeRunning(prior)`，那回答的是
「terraform 该不该建 VM」，而需要的是「有没有 VM 在跑」。`Error` 恰是两者分歧处，
且 console 重启会把所有 `Creating`/`Resuming` 改写成 `Error`——包括 apply 其实
已经成功、agent 正健康运行的。在那里重发会吊销运行中 agent 的证书而**什么也不替换**
（terraform 看到 VM 已符合期望，不重建，新 snippet 永不被读）。
现在只认 `Suspended`/`Released`——terraform 确实执行过销毁的那两个。

### 7.4 真机验证发现的、单元测试不可能发现的

轮换实现完、15 个包全绿之后，真机上仍有三处是死的：

| 缺陷 | 测试为什么发现不了 |
|---|---|
| `ProtectSystem=full` 让 `/etc/qubes-air` 只读，续期写不进去 | 测试写临时目录，没有 systemd 沙箱 |
| CA 签名器类型断言永远失败（接口声明 `*pki.Bundle`，CA 返回 `*SignedCert`） | 所有测试用假签名器，没有跨过真 CA 那道接缝 |
| apt 走公网，每次置备多花 14 分钟，撞执行器超时 | 测试不装包 |

共同点是**测试替身比生产简单**。前两个都表现为「全绿而功能是死的」。

`ReadWritePaths=/etc/qubes-air` 那条尤其值得记：`ProtectSystem=full` 是 agent
**只读证书**时写的，加了写入需求之后它静默失效，报错是深在原子写入路径里的
「read-only file system」，离「systemd」三层远。

### 7.5 还没解决

- **挂起超过 CA 有效期**：CA 十年，暂时不是问题，但半个 CA 凭证被替换的状态没有恢复路径。
- **时钟偏移超过一小时**：中继证书有效期一小时，agent 时钟偏差超过它就连不上，
  且表现为「不可达」，与 VM 死了无法区分。`pki` 的 `NotBefore` 只回拨 5 分钟，
  这是另一个方向的上限。
- **续期无法靠调阈值触发**：抖动是窗口的 25%，窗口随阈值放大，要保证任意 qube
  立刻到期需要阈值 >131%，而上限是 100。测试只能靠做旧注册表记录。

## 8. 未决

- ~~**agent 版本与镜像的耦合**~~：**已决，见 §6**。选了「开机从 artifact store 装」，
  没选不可变镜像。理由不是不可变镜像不好，而是它把每次 agent 升级都变成一次
  重建镜像 + 重建所有 qube，而这个代价始终没量过。代价见 §6.2，新增的信任依赖见 §6.4。
- **artifact store 的认证与 HTTPS**：现在两样都没有，SHA256 钉死是唯一的完整性控制（§6.4）。
  这是已知缺口，不是设计选择——只是还没做。
- **多集群**：CA 现在是 console 全局一个。多个 zone 是共用一个 CA，还是每 zone 一个？
  共用意味着一个 zone 的 CA 泄露波及全部。
- **(d) 的 helper 长什么样**：还没设计。
