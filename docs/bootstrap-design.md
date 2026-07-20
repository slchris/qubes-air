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

## 9. Bootstrap token：首次签发也走 CSR（进行中）

> 状态（2026-07-20）：传输方向已定并已实现——**console 拨 agent，与续期、探测同向**（§9.3）。
> 两侧代码都在，端到端测试跑通了完整链路。**但行为仍然零变化**：cloud-init 还在下发私钥，
> 没有 qube 拿到过 token。剩下的一件见 §9.4，它有一个**必须整体落地**的约束。

§7.1 的续期已经做到「私钥永不过网」，但**首次签发还没有**。今天 cloud-init 投递的是
`/etc/qubes-air/agent-key.pem`，一把和证书同寿命（90 天）的私钥，它至少存在于三个不必要的地方：
console 上渲染出的身份文件、上传到 hypervisor 的 snippet、以及这两者的任何备份。

续期做不到的原因是个先有鸡还是先有蛋：续期由 mTLS 证书认证，而 agent 此刻还没有证书。
bootstrap token 就是用来打断这个循环的——它只授权签发一张证书、只给一个 qube 名、只能用一次、
且很快过期。

### 9.1 已经做完的

- `internal/pki/bootstrap.go`——token 原语。只存 SHA-256 摘要，名字和摘要都做恒定时间比较
  且两个比较无条件都跑（提前返回会让两者在时间上可区分）。
  **一处必须纠正的记录**：上面那句恒定时间比较说的是 `BootstrapRecord.Verify`，而生产路径
  根本不经过它——repository 的 `Redeem` 是按摘要主键的 SQL 查找，时间特性由 B-tree 索引决定。
  实际风险很小（查的是攻击者输入的 SHA-256，索引命中与否的时间差远程测不出来），但把安全性质
  记在一段只有测试在跑的代码头上，正是这份文档到处警惕的那种「全绿而承诺是死的」。
  `Verify` 留着给需要在内存里校验的调用方；声明以 SQL 语句为准。
- `internal/repository/bootstrap_token_repository.go` + `bootstrap_tokens` 表——持久化。
  **兑换是一条语句**：`UPDATE ... WHERE redeemed_at IS NULL AND not_after > ?` 带 `RETURNING`。
  Go 里 Verify 再 Redeem 是 check-then-act，而库跑在 WAL + 25 连接池下，两个 agent 拿同一个
  泄露的 token 可以都通过检查再各自写入，结果是同名两张证书——正是单次性要防的冒充。
  过期判断放同一条语句是同样的理由。
- ~~`internal/handler/bootstrap_handler.go`~~ → **`internal/service/bootstrap.go`**（2026-07-20 搬家）。
  HTTP 壳按 §9.3 摘掉了，逻辑原样搬进 `BootstrapIssuer`。三个顺序都是承重的：
  先兑换后签发（fail-closed：签发失败烧掉 token，好过「证书已发 + token 仍有效」）、
  先注册后返回（注册表才是授权依据，发一张没登记的证书 = 给一个到处看着对、哪都用不了的身份）、
  先校验 CSR 后兑换（畸形请求不该烧掉还能用的 token）。
  CN 只从兑换记录取，绝不从请求体或 CSR 取。

  搬家时修掉一处**文档与代码不符**：最后那条原来是假的。handler 只检查了 `csr_pem` 非空，
  一个内容是垃圾的 CSR 会通过检查、兑换掉 token、然后在签名时才失败。现在 CSR 的语法、
  自签名和「不带 SAN」在兑换前就验完。CN 仍然只能在兑换后判（它就存在兑换记录里），
  所以拿着有效 token 请求别人名字的 CSR **仍然烧 token**——那是越权尝试，fail-closed 是对的。

  同时消失的是「回复不泄露细节」那条：它是给不可信远端准备的，而现在调用方是 console
  自己的拨号器。细节留在进程内的日志里，也就是运维本来就需要它的地方。

### 9.2 一个必须写下来的测试结论

`TestConcurrentRedemptionHasExactlyOneWinner` **抓不住它名义上针对的 bug**。把 `Redeem` 换成
朴素的 SELECT-then-UPDATE，12 并发和 300 并发都照样通过——天然窗口比调度粒度还窄。
插入 5ms 延迟后才失败（12 个全赢），说明测试形状对、灵敏度不行。

所以单次性靠的是**语句结构**（没有窗口可丢），不是那个测试。条件逻辑由顺序测试确定性覆盖。
测试留着是因为几乎不花钱、且能在高负载下抓到粗暴回归，但**改 `Redeem` 要对着语句 review，
不能对着这里的绿灯**。

（CN 来源和注册顺序两条测试做过反事实验证，确实能抓住对应的 bug。）

### 9.3 已决：传输方向——console 拨 agent

> **2026-07-20 已决。** 采纳下面「建议的形状」：与续期、探测同向，console 拨 agent。
> 中间人两头都堵死——冒充 agent 的一侧拿不出 token，冒充 console 的一侧拿不出 CA 签的
> 客户端证书，而带客户端证书的 TLS 握手无法被中间转发（签名盖住握手转录）。
> 促成决策的分析原样保留在下面。

做出这个决定时的现状：handler 是一个 HTTP 端点，挂在 `/bootstrap/certificate`，
**在 `/api/v1` 之外、无认证**（首启的 agent 没有 API token）。它隐含要求 **agent 主动连回 console**。

这个方向是错的，理由有两条：

1. **系统里没有别的东西需要这个方向。** 续期（`certrenew.go:460`）和健康探测
   （`agentprobe.go:267`）都是 console 拨向 `qube.IPAddress`。让 bootstrap 反过来，
   等于凭空多要一条连通性——内网 PVE 无所谓，GCP 就得先有 WireGuard。
2. **靠 WireGuard 打通会绕回原点。** WG 配置得由 cloud-init 送进 guest，里面含 WG 私钥。
   于是「不在 cloud-init 里放私钥」变成了「换一把私钥放」。差别有（WG 私钥只给网络接入，
   不是车队 PKI 身份），但目标被稀释了。想让 guest 自己生成 WG 密钥，又需要一条把公钥送回去的
   通道——同一个循环。

**建议的形状：和续期同方向，console 拨 agent。** 关键在于认证的两个方向不需要靠传输层解决：

- **agent 认 console**——cloud-init 本来就要送 CA 证书（公开的，不是秘密）。
  agent 用它验对方的客户端证书，**验完才交出 token**。
- **console 认 agent**——靠 token。

```
cloud-init 送 {token, ca.pem}     (无私钥)
agent 首启 → 生成密钥对 + CSR, 临时自签证书起监听
console → 拨过去, 出示 CA 签的客户端证书
agent   → 用 cloud-init 的 CA 验 console, 通过后交出 {token, csr}
console → 兑换 token, 签发, 登记, 返回证书
agent   → 验证书链到那张 CA, Identity.Install 落盘
```

第 6 步的原子安装（`identity.go:223`，带 `.commit` 文件和 fsync 目录）续期已经有了，直接复用。

**已按此实现（2026-07-20）：**
- HTTP 壳已摘掉，逻辑进 `internal/service/bootstrap.go`（见 §9.1）。
- agent 侧 `internal/agent/bootstrap.go` + `NewPendingIdentity`，
  `cmd/qubes-air-agent/main.go` 在没有身份时进 bootstrap 模式。
- console 侧 `internal/service/agentbootstrap.go`，与续期同向拨号。
- §9.1 的持久层原样保留，与传输方式无关这条成立。

三处实现时才浮出来、值得记的：

- **`pki.AgentCommonName` 现在是唯一定义**。两侧各自独立算这个名字（console 用它拒绝不匹配的
  CSR，agent 用它写 CSR），两份定义迟早分叉，而症状是每次 bootstrap 都因 CN 不匹配失败，
  两边都不会把它读成命名漂移。
- **只有「一对文件完全不存在」才算首启**。半对、损坏、读不了，一律照旧启动失败——那描述的是
  *曾经有身份而失去了* 的机器，当成首启就是拿一个 token 去掩盖运维需要知道的损坏。
  中断安装的恢复跑在探测之前，所以 commit 与 materialize 之间崩过的机器回来是**已安装**，
  而不是拿着已烧掉的 token 再 bootstrap 一次。
- **console 拨 bootstrap 时不验对端证书**，这是全 console 唯一这么做的地方。它必须如此：
  对端此刻只有自签占位证书。撑住安全性的是**另一侧**——agent 在交出 token 前要求客户端证书
  链到 cloud-init 那张 CA。所以这条不能在任何时候「顺手统一」成验证模式，也不能把这个
  跳过验证的 dial 复用到别处；`internal/transport/grpc/bootstrap_e2e_test.go` 的第二个测试
  就是钉住它的：外来 CA 的客户端和裸连接都必须在任何 token 移动前失败。

### 9.4 还没接的：cloud-init（**必须整体落地**）

agent 侧和 console 侧都已就位，但**行为仍然零变化**——因为 cloud-init 还在下发私钥，
没有任何 qube 拿到过 token，所以拨过去的 bootstrap 永远无事可做。剩下的是：

1. cloud-init 改造——送 token + CA，不送私钥（`internal/service/cloudinit.go:163` 是私钥那行）。
   连带 `CertIssuer.IssueFor` / `ReissueFor` 不再铸证书，改成铸 token。
2. **一个 bootstrap 扫描**——按「running + 有 IP + 注册表里没有证书」找 qube 并拨过去。
3. 签发时机——**每一处重新渲染 user-data 的地方**都要先 `InvalidateForQube` 再 `Issue`：
   create、重新 provision，以及 **resume**。§7.3 说了 resume 会重发身份；挂起几个月后
   旧 token 早已过期，只在 `CreateQube` 铸 token 的话，resume 出来的 qube 拿到的是死 token
   ——又是一次「看起来正常、agent 起不来」。

**这三件必须一起落地，顺序还不能反。** 第 1 件单独上线会让情况**比现在更糟**：qube 拿着
token 起来了，但没有第 2 件就没人来拨，于是每台新 qube 都永远没有证书——正好又制造一次
这份文档从头到尾在消灭的那个形状。安全的顺序是 **2 → 1**：扫描先上，此时没有任何 qube 带
token，扫描扫到的是空集，完全无害；确认它在真机上稳定之后再切 cloud-init。

两个接线时必须对准的数字：

- **token TTL 要按实测置备时长定，不是按直觉。** §7.4 记过 apt 走公网让置备多花 14 分钟；
  一个「很快过期」的 5 分钟 TTL 会让一次慢置备烧掉 token，而这个失败要等 console 拨过去
  才看得见。TTL 至少要盖住「apply 开始 → agent 首启完成」的最坏观测值再留余量。
- **`DeleteSpent` 还没有任何调用方**，接线时记得挂上定时清理，否则表只进不出。

连带好处：`terraform/modules/remote-qube-base/providers/gcp/main.tf:106` 现在硬性要求
identity bucket 必须私有，理由就是「身份文档含 agent 私钥」。换成 token 之后，
泄露的后果从「等于泄露车队身份」降级成「一个短时效、单次、绑定单机的 token」。

## 10. 分环境：§9 定的方向只对 PVE 成立

> 2026-07-20 调查。起因是一个直接的问题：PVE、AWS、GCP、k8s+kata 的 bootstrap 是不是
> 得各做各的。答案是「是，但分界不在协议上」——而调查过程中撞到一个**比 bootstrap 更急**
> 的缺口，见 §10.2。

协议本身（CSR + 单次 token + 原子安装）是provider 无关的，已经写成那样了。
每个环境真正不同的只有两件事：

- **(a) `{token, ca.pem}` 怎么送进 guest**
- **(b) console 能不能拨到 guest**

(a) 早就是分 provider 的，且已经抽象好了。(b) 才是问题所在。

### 10.1 (a) 投递：token 设计让每个 provider 都变轻

| | 机制 | 需要的特权 |
|---|---|---|
| Proxmox | snippet，SFTP 传到节点（`proxmox/main.tf:161`） | **节点 root SSH**（§4） |
| GCP | 私有 GCS object + 开机脚本从 metadata 取 token 再拉（`gcp/main.tf:176`） | 写 bucket；实例 SA 有读权限 |
| AWS | **没有**。`agent_user_data_file` 在 AWS 子模块里连变量都不存在 | — |

token 设计不改变机制，改变的是**载荷**：从「90 天私钥」变成「公开的 CA + 一个单次、
短时效、绑定单机的 token」。这在每个 provider 上都是净收益，而且直接降级两条现有约束：
`gcp/main.tf:106` 要求 identity bucket 必须私有的理由消失大半，§4 里节点 root SSH 的
**后果**也小了一个量级（拿到 snippet 不再等于拿到车队身份）。

它**不**消除 §4 的 SSH 需求：PVE API 至今没有 snippet 上传接口，写任意文件仍然只能 SFTP。
所以「不用 SSH 能不能做完 PVE 的 bootstrap」——不能，§4(a)（专用 key + `command=` 钉死）
仍是近期答案。但这条现在没那么疼了。

### 10.2 (b) 可达性：GCP 今天就是坏的，而且不是 bootstrap 弄坏的

**整个 terraform 树里没有任何防火墙 / security group 资源。** grep 全仓只有一条命中，
而且是注释——`gcp/main.tf:137`「控制台经私有路径 (WireGuard) 拨 agent 的 :8443」。

那条私有路径**没有任何资源在建**。WireGuard 在 `zone-base/main.tf:42` 和
`terraform/variables.tf:145` 里只是几个没人引用的变量，默认空值，注释写着「由后续阶段回填」。

于是 GCP 的实际行为是：`assign_public_ip` 默认 false → 实例没有外网地址 →
`gcp/main.tf:298` 把 **VPC 私网地址**写进 output → console 存下来 →
`agentprobe.go:267` 直接 `net.JoinHostPort(私网IP, 8443)` 从 console 主机拨出去 → 永远超时。

**每台 GCP qube 都会置备成功，然后永远报 unreachable。** 这比 AWS 那个诚实的空壳
（88 行，全是注释掉的 TODO，什么都不建）难查得多——又是这份文档从头到尾在消灭的那个形状。

这件事**先于** bootstrap：探测和续期今天就撞在上面，bootstrap 接上去只是第三个受害者。

### 10.3 一个必须记的推论：§9.3 的论证在 GCP 上有个洞

§9.3 选 console→agent 的第一条理由是「系统里没有别的东西需要 agent→console 这个方向，
续期和探测本来就是 console 拨 agent」。这条**成立**——bootstrap 确实没有新增连通性要求。

但它顺带假设了那个要求**已经被满足**。在 PVE（同一 L2）是满足的；在 GCP **不满足**，
而且没有任何东西在满足它。§9.3 还专门论证过「靠 WireGuard 打通会绕回原点：WG 配置得由
cloud-init 送进 guest，里面含 WG 私钥」——那段是用来反驳 agent→console 的，
**但它对 console→agent 同样成立**，只是下沉了一层。§9.3 没注意到这一点。

结论不是方向选错了，是**这个方向只对 PVE 是免费的**。

### 10.4 正确的分界：有没有可信证明，代码里早就写了

`internal/pki/bootstrap.go:48` 自己写着：**在提供签名实例证明的 provider 上，
那个证明应当取代 token，而不是和它并存。** 这就是真正的分环境轴，而且没人动过：

| | 可用的证明 | bootstrap 该长什么样 |
|---|---|---|
| **PVE** | **没有** | token 是唯一选择，保持现状 |
| **GCP** | instance identity token（metadata server 签发，Google 签名，带 audience、实例 ID） | 用它，cloud-init 里**不放任何秘密** |
| **AWS** | instance identity document + PKCS7 签名 / IMDSv2 | 同上 |
| **k8s+kata** | projected ServiceAccount token + TokenReview | 四者里最强：API server 是在线权威，token 短时效且绑 audience |

这条推论比看起来重要：**有了证明，agent 就能在没有预共享秘密的情况下自证身份**，
于是 agent→console 方向变安全了——而那个方向**根本不需要入站可达性**。

所以 §10.2 的 GCP 缺口有两条出路，而不是一条：要么把 WireGuard 真建出来（并接受
WG 私钥进 cloud-init），要么**在云上翻转方向**，让 agent 带着云厂商签名的证明拨回 console。
后者同时解决可达性和「秘密进 user-data」两件事，代价是 console 需要一个可达端点
（但它本来就有 HTTP 服务）。

**建议：PVE 保持 console→agent + token；云上走 attestation + agent→console。**
两条路共用同一套 `BootstrapIssuer`（兑换换成验证证明即可）和同一套 agent 侧安装逻辑。

### 10.5 packer：不需要结合，这是 §6 特意拆开的

`packer/scripts/install-agent.sh` 已经退役成硬失败，镜像里只留 debian-12 +
`qemu-guest-agent`。所以「打模板生成 key」这件事**恰恰是不能做的**——模板是所有 qube 共享的，
里面任何 per-qube 秘密都等于「一台被攻破 = 全部被攻破」（§6）。token 设计让这条更干净：
模板里现在连证书都没有。

顺带：`packer/templates/fedora.pkr.hcl` 是死的——从没跑过，`iso_checksum` 至今是
`sha256:xxxxx`，而且是 Fedora，跟现网的 `debian-12-cloudimg-template` 对不上。
它该删，留着只会让人以为镜像构建是通的。

### 10.6 k8s + kata 不是 bootstrap 的一个变体，是第三种 zone

没有 cloud-init，没有 terraform 的 VM 生命周期，`compute_running` 的挂起/恢复没有对应物
（Pod 不是那么工作的），存算分离映射到 PVC + Pod。身份走 projected SA token。

也就是说它复用的是**协议和 agent 侧**，不是 provider 模块那一层。当前仓库里零基础，
应当按「新增 zone type」估工作量，不要按「再写一个 provider 模块」估。

### 10.7 现状快照（2026-07-20 实测）

| | terraform | console 接线 | 能不能真跑 |
|---|---|---|---|
| Proxmox | 322 行，完整 | 完整 | ✅ 真机验证过 |
| GCP | 308 行，资源都是真的 | 部分（`renderGCP` 有，容量报告是占位） | ⚠️ 能建起来，然后永远不可达（§10.2） |
| AWS | 88 行，纯空壳，资源全注释掉 | **只有名字**，没有 `renderAWS`，没有凭证注入 | ❌ 什么都不建 |
| k8s+kata | 不存在 | 不存在 | ❌ |

`scheduling.go:179` 那句注释说「GCP/AWS 模块还是不建任何资源的骨架」——对 GCP 已经过时了，
而这个过时正好掩盖了 §10.2：它让人以为 GCP 还没开始，实际上它已经能建出永远连不上的机器。
