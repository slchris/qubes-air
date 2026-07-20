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

### 4.1 更新（2026-07-20）：上面这份清单不完整，(c) 的结论也过时了

> 起因是一个直接的质疑：「一定要 SFTP 吗，这么 SSH 过去不够优雅」。查完之后：
> **SSH 确实是 snippet 的唯一路径，但 snippet 不是唯一的投递路径。**

**先确认坏消息。** PVE 至今没有 snippet 上传 API，**升级到 PVE 9 也没有**：

- Bugzilla #2208（「新增管理 snippets 的 API 端点」）2019 年提出，至今 UNDECIDED。
- 最新的 PVE **9.2**（2026-05）roadmap 里搜不到任何 snippet 相关条目。
- bpg 的对应 issue #2112 已经 **closed as wontfix**——provider 补不了 API 缺的东西。

所以「为了去掉 SSH 而升 PVE 9」是白升。（本集群另有一层：node4 还在 8.0.3、其余 8.2.2，
升 9 之前得先全部拉到 8.4，那本身是个独立工程。）

**但 (c) 的判断错了，因为它只考虑了 cloud-init drive 的原生字段。** bpg 文档写得很明确：

> `iso`、`vztmpl`、`import` 三种 content type **always use the HTTP API**；
> `snippets` 和 `backup` 才需要 SSH。

也就是说——**「PVE API 不能传任意文件」是假的，只有 snippet 这一种内容不能传。**
于是多出三条 §4 当初没想到的路：

- **(e) 自己造 NoCloud ISO，走 `content_type = "iso"` 经 API 上传，当 CD 挂上去。**
  这正是 PVE 内部的做法——cicustom 也只是「拿 snippet 的内容去生成同一个 cidata ISO」，
  PVE 9 roadmap 里那句 "Enable Joliet for cloud-init disks to avoid issues with the
  nocloud ISO format" 就是在说这个 ISO。
  代价：console 要能生成 ISO9660（Go 库或 `xorriso`）；而且**必须摘掉模板带来的
  cloud-init 盘**，否则两个 `cidata` 卷同时在，cloud-init 选哪个是不确定的。
  本集群这条反而比现状更稳：`local` 声明了 `iso`，却**没有**声明 `snippets`
  （现在能用纯属 `pvesm path` 恰好解析得出来）。
- **(f) 把 snippets 放到 console 能写的共享文件存储**（NFS/CIFS）。console 直接写文件，
  节点只读。代价：本集群目前只有 RBD（block，仅 `images`）和节点本地 dir，没有文件型共享存储，
  等于新增一套基础设施。附带好处：PVE 要求 cicustom 文件在**所有可能迁移到的节点**上都存在，
  而现在 snippet 躺在节点本地 `local` 上——这条今天就是潜在的坑。
- **(g) 经 QEMU guest agent 写文件**：`POST /nodes/{node}/qemu/{vmid}/agent/file-write`，
  纯 API，权限只要 `/vms/{vmid}` 上的 `VM.Monitor`，内容上限约 60 KB（pveproxy 的 post 上限）。
  限制是**只能开机后写**——但在 token 设计下这不是问题，反而是优点，见下。

**还有一条必须写下来的、比「要 SSH」更强的约束**：bpg README 那句容易被略过的话是
「snippet 上传需要 **PAM 账号**（真实 Linux 账号）」。也就是说 **API token 无论授多大权限
都做不了这件事**——它是文件系统写入，不是 API 调用。所以 §4 那句「console 需要每台
hypervisor 的 root」不只是权限大，而是**认证轴都不同**：api_token 和 SSH 私钥是两套凭证，
前者永远替代不了后者。这也解释了为什么 bpg 的 issue #2112 只能 wontfix。

顺带一个佐证：bpg 3793 行 changelog 里，snippet 的传输方式**只在 SSH 内部演进过**
（shell 管道 → 0.105.0 的正式 SFTP），从未出现过任何 API 路径。

### 4.2 (g) 值得单独说：它能让 SSH 从「每次置备」降级成「装集群时一次」

token 设计把每台 qube 独有的秘密缩到了 `{token, ca.pem}`，而 cloud-init 里剩下的东西
（agent 包 URL + SHA256、安装脚本、`agent.env`）**对整个车队是同一份**。于是可以拆开：

```
一份静态 vendor snippet（全车队共用，装集群时传一次）
        ↓  cicustom vendor=local:snippets/qubes-air-vendor.yaml
   装 agent、写 agent.env、起服务
        ↓
console 经 API file-write 把 {token, ca.pem} 写进 VMID N   ← 每台一份, 无 SSH
        ↓
console 拨过去跑 bootstrap
```

几个性质刚好对上：

- **VMID 就是绑定关系。** console 通过 hypervisor API 把 token 写给「VMID N」，
  这本身就是「这个 token 属于哪台机器」的凭据——正是 token 需要的绑定。
- **qemu-guest-agent 不是新依赖**，terraform 本来就要靠它拿 IP（§6）。
- **agent 名字不需要每台定制**：`cmd/qubes-air-agent/main.go:87` 已经在没有
  `--remote-name` 时回退到 hostname，而 hostname 可以走 PVE 原生 cloud-init 字段。

**一个看起来会否掉 (g) 的先有鸡还是先有蛋，其实不成立。** 直觉上「QGA 是 snippet 装的，
所以没 snippet 就没 QGA」——但 §6 早就要求**镜像里必须有 `qemu-guest-agent`**
（没有它 terraform 等不到 IP，apply 会挂到超时，真机实测过）。cloud-init 里那行
`packages: - qemu-guest-agent` 是防御性的重装，不是唯一来源，`cloudinit.go` 自己的注释
写着「The image normally has it」。所以 (g) 依赖的是镜像里本来就该有的东西。

代价也要说清楚：置备从「一次声明式 apply」变成「apply → 等 guest agent → 推 token → 拨」的
多步编排；QGA 的 `guest-file-write` 在某些发行版的默认配置里是被 block 掉的，得实测；
而且 60 KB 上限对 `{token, ca.pem}`（约 2 KB）绰绰有余，但这条路上永远不能塞大东西。

**另有一个今天就存在、(e)(f)(g) 都能顺手修掉的隐患**：`main.tf:288` 对 `node_name`
做了 `ignore_changes`，好让 PVE 的 HA/CRS 自由迁移 VM；而 snippet 躺在**迁移前那个节点**的
本地 `local` 上。首启之后无害（cloud-init 只读一次），但**重新 provision 会踩到**——
PVE 自己的文档也要求 cicustom 文件在所有可能迁移到的节点上都存在。

### 4.3 现在的判断

**SSH 的「疼」已经比 §4 写的时候小了一个量级**：它运的从「90 天私钥」变成了
「公开的 CA + 一个单次、短时效、绑定单机的 token」。拿到 snippet 不再等于拿到车队身份。

排序（2026-07-20 定稿）：

1. **(g) 是要做的那条。** console 什么都不用加——它已经在和 PVE API 说话了：不用新包、
   不用新端口、不用挂载、不用碰模板。而 VMID 本身就是 token 需要的绑定关系。
2. **(a) 在 (g) 落地前先做**，成本最低，今天就能把 SSH 收窄。
3. **(e) 是备选**，不引入新基础设施，但要自己写 ISO 生成。
4. **(f) 已否决**（console 侧）。它要求 ceph 客户端进持有车队 CA 的那个 qube，
   而那是方案前提、不是实现细节。PVE 侧那半已完成且有用，见 §4.4/§4.5。
5. **(d)** 在 (g) 成立后就没必要了。

**(c) 原来的结论作废**——它当年只看了 cloud-init drive 的原生字段，漏掉了
「API 能传 ISO」和「API 能经 guest agent 写文件」这两件事。

### 4.4 (f) 的实施计划 —— **PVE 侧已完成，console 侧已放弃**

> **2026-07-20 结论变了，先说结论再留计划。**
>
> PVE 侧全部做完并验证过（见 §4.5）。**但 console 侧不做了**，因为 (f) 的定义就是
> 「console 直接把文件写进共享存储」，而往一个文件系统写文件就必须先挂上它——
> 于是 ceph 客户端必须进 console 那台持有车队 CA 的 qube。那不是实现细节，是这个方案
> 本身的前提，而这个前提不可接受。
>
> 绕过挂载的路都查过了，没有：`go-ceph`/`libcephfs` 不用内核挂载但仍要把库放进那个
> qube；集群跑着的 RGW 是对象存储，不在 CephFS 命名空间里，PVE 读不到；让 terraform 去
> 写也没用——它和 console 在同一个 qube，而且 bpg 对 `content_type = snippets`
> **永远走 SFTP**，换成 cephfs 存储一样。
>
> **替代它的是 (g)**，见 §4.2：console 什么都不用加。
>
> 下面的原始计划保留，因为 PVE 侧那半确实按它做完了，而且那个「哈希进文件名」的结论
> 对任何「文件已经在位、terraform 只引用」的投递方式都成立。

**目标**：console 把 snippet 直接写进 PVE 节点已经能读到的地方，于是 terraform 再也不需要 SSH。

#### 一个必须先说的坑：(f) 会把 §7 那个 bug 原样带回来

今天是 `proxmox_virtual_environment_file` 上那句 `checksum = filesha256(...)` 让 terraform
依赖**内容**。§7 记着它的来历：没有它，真机上出现过「console 渲染了新身份、节点上还是旧
snippet、cloud-init 照旧文件跑完并报 done」，而且**证书轮换会永远传不下去，每次 apply 都报成功**。

(f) 把这个资源整个删掉，terraform 只拿到一个 volume ID 字符串。**字符串不会因为文件内容变了
而变**——于是 §7 那个 bug 一字不差地回来了，而且这次连 `checksum` 这个补丁都没地方挂。

**解法：把内容哈希放进文件名。**

```
qubes-air-<name>-<sha256 前 12 位>.yaml
```

内容变 → 文件名变 → volume ID 变 → `user_data_file_id` 在 VM 上是 ForceNew → 计算实例重建
→ cloud-init 读到新文档。行为和今天完全一致，但这次是**由构造保证**的，不是靠记得传 checksum。
附带好处：身份文档变成内容寻址的，旧的一份永远不会被就地覆盖。

代价是旧文件会堆积，console 要负责回收（qube 删除时、以及重新 provision 成功后只留当前那份）。

#### 存储选型

**存储选 CephFS 而不是 NFS**（这条只关系到「如果要用共享存储，选哪个」——
而 console 侧已经不走这条了，见本节开头）：外部 Ceph 集群本来就在
（mon `10.31.0.77/78/79`），PVE 原生支持 CephFS
存储类型且允许 `snippets` content，而且它是**真共享**——顺带修掉 §4.1 提的那个迁移隐患
（现在 snippet 躺在节点本地 `local`，而 `main.tf:288` 对 `node_name` 做了 `ignore_changes`）。

**今晚第一件要验的**：那个 Ceph 集群**有没有跑 MDS / 建过文件系统**。纯 RBD 的集群通常没有。
节点上 `ceph fs ls` 一句就能知道。没有的话要么建 CephFS（起 MDS），要么退到 NFS。

NFS 是退路而不是首选：多一个 SPOF，且认证比 cephx 弱得多。真要用就配 `root_squash` + IP 白名单。

#### 步骤

1. `ceph fs ls` —— 确认有没有 CephFS。没有则决定「建 MDS」还是「退 NFS」。
2. PVE 里加这个存储，**显式声明 `snippets` content type**。
   （注意现在的 `local` 其实**没有**声明 snippets，能用纯属 `pvesm path` 恰好解析得出来——
   新存储别再靠这个巧合。）
3. 每个节点上 `pvesm path <ds>:snippets/probe.yaml` 验证解析得出来。
4. 在 console 跑的地方挂上这个共享（console 是 Qubes AppVM 里的 systemd 服务，
   只有 `/rw` 持久，所以挂载要写进 AppVM 的持久配置而不是手工 mount）。
5. 代码：`AgentIdentityDir` 指向挂载点；`SnippetVolumeID`（**现在是死代码，只有测试在用——
   它就是为这件事写的**）加上内容哈希参数；proxmox 模块删掉
   `proxmox_virtual_environment_file.agent_identity`，`user_data_file_id` 改成变量传入。
6. 确认还有没有别的东西需要 provider 的 `ssh` 块。如果没有，整个块可以删——**那才是这件事的收益**。

#### 两个要当场验、不要假设的

- **共享挂了会怎样。** cloud-init 只在首启读一次，但 PVE 在 VM **启动时**会按 cicustom
  重新生成 cidata ISO。所以共享不可用可能不只是「建不了新 qube」，而是「现有 qube 起不来」。
  这条决定了它是不是新的 SPOF，必须实测。
- **谁能读到。** snippet 里是 token。token 设计下它是单次、短时效、绑定单机的，比 90 天私钥
  好得多，但共享目录仍然不该整个局域网可读。cephx key 限定子树，或 NFS 白名单。

#### 落地方式

**走配置开关，保留 SFTP 那条路作为回退。** 这条链路今天在真机上是通的，(f) 动的正是它，
一次性切换没有退路可言。

### 4.5 PVE 侧实测记录（2026-07-20，真机）

这半边做完了，留着是因为结论对 (g) 也有用，而且这些数字下次不用再测一遍。

**Ceph**（外部 cephadm 集群 reef 18.2.4，`user@10.31.0.77`）
- `ceph fs volume create qubesair --placement=2` → 池 `cephfs.qubesair.meta`/`.data`，
  MDS 1 主（ceph-03）+ 1 备（ceph-01）。
- **`mds_cache_memory_limit` 在建之前就设成 256 MiB**。默认 4 GiB 对几 KB 的
  cloud-init 是荒谬的，而这三台还跑着 mon+mgr+osd+rgw。先设再建，守护进程就不会
  以 4 GiB 起来过。
- 客户端 `client.pve-snippets`，权限 `allow rw fsname=qubesair`——**碰不到装 VM 的
  那些 RBD 池**。

**PVE**
- `pvesm add cephfs cephfs-snippets --monhost '10.31.0.77 10.31.0.78 10.31.0.79'
  --username pve-snippets --fs-name qubesair --content snippets --keyring <file>`
- **坑**：`pvesm` 拒绝预置好的 `/etc/pve/priv/ceph/<id>.secret`（报 already exists），
  必须用 `--keyring` 让它自己写。
- **六个节点全部 active 且已挂载**，从每个节点读到的是同一份（md5 一致）。

**验证做到哪一步（这段是重点）**
- `qm cloudinit dump` **不是有效判据**——它按设计显示**自动生成**的配置，完全忽略
  cicustom。差点被它骗过去。
- 有效判据是让 PVE 真正构建 cidata ISO，那才会读文件。一次性 VM 上做的反证：
  文件在 → `generating cloud-init ISO`，exit 0；文件挪走 →
  `volume '...' does not exist`，exit **2**；放回 → 又成功。
- 所以 **PVE 的 cloud-init 路径确实能从 CephFS 读到 snippet**，这条对任何
  「文件已在位、terraform 只引用」的方案都成立。

**顺带暴露的一个风险，仍未直接验证**：文件不在时 PVE 是**硬失败**的，而 PVE 在
**VM 启动时**会按 cicustom 重新生成 cidata ISO。所以共享存储不可用很可能不只是
「建不了新 qube」，而是**现有 qube 重启不来**。真要依赖任何共享存储投递之前，
必须把它下线试一台 VM 重启。

**(g) 能用上的那部分**：§4.2 里那份「全车队共用的静态 vendor snippet」正好可以放这儿，
装集群时传一次，之后再也不动。那份文件里没有任何 per-qube 秘密，所以谁写它都无所谓。

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

**`packer/templates/fedora.pkr.hcl` 已于 2026-07-20 删除**，因为它不只是没用，而是个陷阱：
Fedora 39（现网模板是 `debian-12-cloudimg-template`）、`iso_checksum` 至今是 `sha256:xxxxx`
所以根本跑不起来，而且——最要命的——**它不装 `qemu-guest-agent`**。也就是说把前两个问题修好
反而更糟：会产出一个看起来构建成功、但 terraform 永远等不到 IP、apply 挂到超时的镜像，
正是本节开头说「这次验证实测过」的那个故障。

没有删 `packer/scripts/install-agent.sh`，理由相反：还有东西可能引用它，留着硬失败才拦得住。
模板文件没有任何引用，删掉不会让任何构建「静默成功」。

镜像构建目前**没有**自动化路径（§2 的前提表里「模板 VM」那行仍是「不存在 / 系统生成」）。

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

> 状态（2026-07-20）：**做完了，行为已经变了。** 传输方向已定并实现（§9.3，console 拨 agent），
> 两侧代码都在，端到端测试跑通完整链路，扫描器和 cloud-init 改造都已落地。
> cloud-init **不再下发私钥**——它送的是公开 CA + 单次 token，证书由 agent 自己生成密钥、
> console 拨过去签 CSR 产生。剩下的是真机验证，见 §9.5。

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

### 9.4 已接完（2026-07-20）

按下面记的顺序落地了，**扫描先上、cloud-init 后切**：

1. cloud-init 改造——送 token + CA，不送私钥（`internal/service/cloudinit.go:163` 是私钥那行）。
   连带 `CertIssuer.IssueFor` / `ReissueFor` 不再铸证书，改成铸 token。
2. **一个 bootstrap 扫描**——按「running + 有 IP + 注册表里没有证书」找 qube 并拨过去。
3. 签发时机——**每一处重新渲染 user-data 的地方**都要先 `InvalidateForQube` 再 `Issue`：
   create、重新 provision，以及 **resume**。§7.3 说了 resume 会重发身份；挂起几个月后
   旧 token 早已过期，只在 `CreateQube` 铸 token 的话，resume 出来的 qube 拿到的是死 token
   ——又是一次「看起来正常、agent 起不来」。

**这三件必须一起落地，顺序还不能反**，当时是这么记的，也是这么做的：第 1 件单独上线会让
情况**比现在更糟**——qube 拿着 token 起来了，但没有第 2 件就没人来拨，于是每台新 qube 都
永远没有证书，正好又制造一次这份文档从头到尾在消灭的那个形状。所以扫描先上（那时没有任何
qube 带 token，扫到的是空集，完全无害），再切 cloud-init。

一个实现时才定下来的数字：**token TTL 取 1 小时**。§7.4 记过一次置备光 apt 就花了 14 分钟，
凭直觉设的 5 分钟会在一次慢启动里过期，而这个失败要等 console 拨过去被拒才看得见——
那时 apply 早就报成功了。

> **投递方式的现状（2026-07-20）**：仍然是 SFTP snippet。CephFS 那条（(f)）
> PVE 侧验证通过但 console 侧已否决，替代它的是 §4.2 的 (g)。§9.5 的验证
> **刻意先走 SFTP**：(g) 改的是投递机制，而这里验的是投递内容和 bootstrap 流程，
> 先在能用的机制上验完，之后换机制出问题才分得清是哪一层。

### 9.5 真机验证：通过（2026-07-20）

在 infra 集群上跑了一台一次性 VM（克隆模板 901），走**生产渲染器**产出的 cloud-init，
用**真实的 `AgentBootstrapper` + `BootstrapIssuer`** 拨过去。逐条对照当初列的判据：

1. **qube 建出来时没有证书** ✅ 首启 `/etc/qubes-air/` 里只有 `ca.pem`(0644)、
   `bootstrap-token`(0600)、`agent.env`——**没有 agent.pem/agent-key.pem**。
2. **agent 进 bootstrap 模式** ✅ 日志 `no identity installed; awaiting bootstrap`，
   8443 起了占位监听。旧 agent 会因缺 cert/key 直接 Fatalf；新的没有。
3. **装完不重启就换真证书** ✅ 拨号返回 `status=ok`，落盘 `agent.pem` 的 SHA256 指纹
   `c54f5f3a…` == console 注册的那张。证书文件时间戳正是 bootstrap 那一刻。
4. **token 被抹、重启进普通模式** ✅ `onInstalled` 删掉了 `bootstrap-token`（GONE），
   重启后日志变成 `identity : agent-bootstrap-probe (expires 2026-10-18)`，不再 pending。
5. **`ProtectSystem=full` + `ReadWritePaths=/etc/qubes-air` 对 bootstrap 写入成立** ✅
   ——这条当初特别标注为「新写入方、没验过不算数」（§7.4）。装证书**和**删 token 都穿过了
   systemd 沙箱，没有出现 §7.4 那种 read-only file system。

**两个诚实的旁注**：
- 这次没走 console 的 `BootstrapMonitor` 扫描，是手工把 token 塞进真 token 库、直接调
  `AgentBootstrapper.Bootstrap`。扫描本身有单测覆盖；真机验的是**拨号→签发→落盘→热加载**
  这条单测替身够不到的链路。
- 也没走真 console 进程（它在 Qubes AppVM 里，本机够不到），用的是把生产组件拼起来的
  一次性 e2e 测试。CA 是测试自签的，不是 console credential store 里那个——但签发/验证
  路径是同一份代码。

**§9 到此在真机上闭环。** 剩下的是把它接进真 console（填那三行 agent 包配置 + 让
`BootstrapMonitor` 自动扫），以及投递机制从 SFTP 换到 (g)——都不影响这条已验证的核心链路。

两个已定的数字：token TTL 取 1 小时（§7.4 记过一次置备光 apt 就 14 分钟，5 分钟会中途过期）；
`DeleteSpent` 启动时跑一次、保留 30 天（表按每次置备一行增长，是人的速度，不值得单开 goroutine）。

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

## 11. WireGuard：按 zone 可选的一层，而不是每台 qube 的事

> 2026-07-20 调查。起因：「GCP 和 AWS 是类似的，甚至 PVE 后续也可以选择要不要 WireGuard，
> 这些都是可以选的」。查完之后——**这个判断是对的，而且仓库里早就是这么设计的**，
> 只是从来没接上。更要紧的是：它顺带**取消了 §9.3 对 WireGuard 的那条反对意见**。

### 11.1 先纠正一句我自己写错的话

这一节初稿写的是「仓库里早就是这么设计的，只是没接上」。**这句是错的**，而且错得正是本文
一直在清除的那一类：把**被否决过的**东西当成**没做完**的东西。

真相是：**hub-and-spoke 的 `sys-remote` + WireGuard 网关方案被评审否决过。**

- `docs/architecture.md` 开头就标着 **DEPRECATED**：「该方案在官方 RemoteVM 落地**之前**设计
  （评审证明其方向错误：把 relay 当网络网关、违反平面分离）」。
- `salt/qubes-air/README.md` 记着 `sys-remote/` 被删的理由：「开 `ip_forward` +
  `provides_network` 把 Relay 当本地网关，违反平面分离」。
- `docs/remotevm-alignment.md` 把 WireGuard **降级为「可选底层链路加密」，而非协议本身**。
- `docs/remotevm-selfcheck.md` 的 E3 甚至把「**确认无 WireGuard**」列成验收项。

所以 `zone-base` 和 ansible 里那些 WireGuard 变量**是那个被否决方案的残骸**，不是待接的线头。

### 11.2 但「按 zone 可选」这个说法仍然成立——被否决的是另一件事

被否决的**具体**是什么，很要紧：是**一个 Qubes AppVM 开 `provides_network` + `ip_forward`，
给本地其他 qube 当 NetVM**。那违反平面分离，理由充分，不该复活。

它**不**等于「console 主机有一条到远端 zone 私网的路由」。区别在于：

- 被否决的：sys-remote 成为**别的 qube 的网关**，本地流量平面被一个 relay 穿透。
- 仍然可行的：console 所在的 AppVM 为**它自己的出站流量**持有一个 wg0。它不给任何人当
  NetVM，`provides_network` 保持 false，平面分离不受影响。

而且这条与 selfcheck 的 E3 也不冲突：E3 查的是**远端 qube** 上没有 wg 接口——按 zone 网关走，
qube 本来就不碰 WireGuard，wg 在网关上。两者可以同时为真。

`remotevm-alignment` 那句「降级为可选底层链路加密」，恰好就是这个位置：
**不是协议，是一条可选的链路**。

### 11.3 拓扑：每个 zone 一个网关，qube 不碰 WireGuard

残骸本身指向的形状是对的（这是它唯一还有价值的地方）：

- `zone-base` 的 `wireguard_pubkey` / `vpn_endpoint` 是 **zone 级**变量，不是 per-qube。
- ansible inventory 的分组叫 `zone_admins`，每个 zone 一个 `wireguard_address`
  （`proxmox-zone` → `10.200.2.1/24`，注释掉的 `gcp-zone` → `10.200.3.1/24`）。
- `wireguard_network` 是 `10.200.0.0/16`，每 zone 一个 `/24`。

即：**只有网关持有 WG 私钥，且在网关本机生成；qube 完全不参与 WireGuard**，
它们待在 VPC / 子网里，console 经隧道路由到它们的私网地址。

**这取消了 §9.3 对 WireGuard 的那条反对意见。** §9.3 担心「WG 配置得由 cloud-init 送进
guest，里面含 WG 私钥，于是『不在 cloud-init 里放私钥』变成『换一把私钥放』」——那是在反对
**per-qube** 的 WireGuard。按 zone 网关走，这件事根本不会发生：**没有任何 qube 见过 WG 密钥**，
token 设计一点也没被稀释。

### 11.4 console 侧零改动

`agentprobe.go:267` 拨的是 `net.JoinHostPort(qube.IPAddress, 8443)`，而 GCP 模块
（`gcp/main.tf:298`）记录的本来就是私网地址。**只要 console 所在主机有一条到该 zone 私网段
的路由，这就直接通了**——不需要改任何 Go 代码。

所以 §10.2 那个「GCP 置备成功然后永远不可达」，是**纯基础设施缺口**，不是代码缺口。

### 11.5 可选性是天然的

zone 要么有到自己 qube 的路由，要么没有。同一个开关，同一个形状：

| | 需要 WG 吗 |
|---|---|
| PVE 同一 L2（现状） | 不需要，console 本来就能路由 |
| PVE 异地 / NAT 后 | 需要，网关放在那个 LAN 里 |
| GCP | 需要，网关是 VPC 里一台实例 |
| AWS | 同上 |

### 11.6 现在实际有什么：变量齐全，什么都没建

**terraform**
- `zone-base` 声明了 `wireguard_pubkey` / `vpn_endpoint`，而 `main.tf` 里三处 zone 实例化
  **一次都没传**（`grep -c wireguard_pubkey terraform/main.tf` = 0），所以永远是空串。
- `terraform/variables.tf` 的 `wireguard_config`（`listen_port` / `network`）
  **没有任何模块引用**，只喂给 `outputs.tf`。
- **没有任何 provider 有网关资源**，也没有任何防火墙规则（§10.2 已记：全树零防火墙资源）。

**ansible**（`playbooks/bootstrap-zone.yaml`）
- 装 `wireguard-tools`、在网关本机生成私钥、回显公钥。**到此为止**。
- **它不写 `/etc/wireguard/wg0.conf`**——没有 `[Interface]`、没有 `[Peer]`、
  没有 Address / AllowedIPs / 路由。
- `ansible_host: pve.example.com` 是占位符，这个 play 从没跑过。

**salt**
- `pillar/default.sls` 有 `wireguard_enabled: false`，没有消费方。
- `pillar/secrets.sls` 有一整段 peer 配置（`10.200.1.1`、`allowed_ips: 10.200.2.0/24`、
  `persistent_keepalive: 25`、`REPLACE_WITH_ACTUAL_PRIVATE_KEY`），**没有任何 state 读它**。
- README 里提到 `tailscale/install.sls`，属于**另一个仓库**，而且是被引用来说明「那两处注释
  是错的」。本仓库里 tailscale 零实现——别被那一行带偏。

**还在跑但指向不存在东西的脚本**（比变量更危险，因为它们会「成功」）
- `crypto/scripts/rotate-keys.sh wg` 轮完密钥后指示「重应用 salt sys-remote.wireguard」——
  那个 state **已经被删了**。
- `dom0-scripts/manage-qubes-air.sh` 有 `vpn-status` / `connect` / `disconnect`，
  操作的是 `sys-remote` 里的 `wg-quick@wg0`——那个 qube 和那个服务都不存在。
  而同目录的 `init-qubes-air.sh:129` 写着「WireGuard 方案已废弃, 改用 SSH transport」，
  两个脚本互相矛盾。

**readme.md 里的假陈述**（这是最多人读的文件）
- `:667-670` 列出 `sys-remote/wireguard.sls`、`gateway.sls`、`firewall.sls`、`files/wg0.conf.j2`
  并标注「**真实可用**」——这四个文件**都不存在**，已随 `sys-remote/` 一起删除。
- `:1058` 有一个打勾的 `- [x] Salt States 基础框架（sys-remote WireGuard/网关/防火墙，真实可用）`。
- 而 `:31` 又正确地写着该方案已退役——同一个文件自相矛盾。

### 11.7 这轮修掉的两个具体问题

- **私钥被读回 ansible 变量**。原来那个任务以 `cat /etc/wireguard/private.key` 结尾并
  `register: wg_private_key`，而这个变量**从没被用过**。它把私钥放进 Ansible 的结果数据里：
  `-v` 会打印它，任何 callback 或日志都会留下它。而「在目标机生成」的全部意义
  （`zone-base` 正是为此删掉了 terraform 里的密钥生成）就是密钥不离开那台机器——读回来
  和在别处生成一样彻底地毁掉这个性质。现在只生成、不回读，且用 `umask 077` 而不是事后
  `chmod`，让文件**从不曾**以默认权限存在过。
- **一个永远不会触发的 handler**。`Restart WireGuard` 没有任何任务 `notify` 它；就算触发了
  也没用，因为根本没有 wg0.conf。一个「重启了没有配置的 unit 并报成功」的 handler，
  正是这个项目一直在清除的那种失败形状，所以删掉并写明为什么。

### 11.8 要接通还缺什么

按依赖顺序：

1. **网关本身**。GCP/AWS 各需要一台实例 + 一条放行 `51820/udp` 的防火墙规则 + 开 IP 转发；
   PVE 异地场景需要一台 LAN 内的机器。**现在一个都没有。**
2. **`wg0.conf` 的生成**。peer 集合、地址、`AllowedIPs`（要覆盖该 zone 的 VPC CIDR，
   否则隧道通了但到不了 qube）、以及网关上的转发/路由。
3. **公钥回传**。playbook 现在只是 `debug:` 打印出来就结束；没有任何东西把它写回
   `zone.wireguard_pubkey`。今天是纯手工。
4. **console 侧的 peer**：console 自己的 WG 密钥与接口。
5. **wg0 放在哪**——console 跑在 Qubes AppVM 里，所以隧道要么在这个 AppVM 内，
   要么在一个专门的 sys-vpn qube 里、由 console 用它作 NetVM。这是 Qubes 特有的决定，
   而且**先做这个决定**，因为它决定了第 4 步的密钥归谁保管。

### 11.9 和 §10.4 的关系：两条路，不冲突

§10.4 提过另一条出路：云上用厂商签名的实例证明，让 **agent 拨 console**，从而完全不需要
入站可达性。那条和本节不冲突，取舍是：

- **WireGuard**：改动全在基础设施，console 零改动，对四种环境形状一致；代价是多一层要运维
  的网络，以及每个 zone 一个网关的成本。
- **Attestation + 反向拨号**：不需要隧道也不需要网关；代价是每个 provider 各写一套证明验证，
  且 PVE 没有可用的证明原语，所以 PVE 仍要留着现在这套。

如果目标是「四种环境一个形状」，WireGuard 更整齐——这也是仓库原本的选择。

## 12. WG 密钥放哪、谁来轮换、以及组网为什么必须分 provider

> 2026-07-20，三个相关的判断：「不要写这种奇怪的脚本去 rotate key，还是写在代码里比较合适」、
> 「wg 的这个存放在哪里，我理解就是单独的一个 qube 来存放的」、
> 「组网还是要看云的能力的」。三个都对，但第二个有一处技术差别必须说清楚。

### 12.1 轮换不该是 shell 脚本，理由有三条

`crypto/scripts/rotate-keys.sh` 里的 `rotate_wg` 已删除。过时的那条指示
（「重应用 salt sys-remote.wireguard」，而那个 state 已随 `sys-remote/` 删掉）只是表症，
真正的问题是这件事的形状不对：

1. **它是第三套凭据机制。** console 已经有加密凭据库（AES-256-GCM + 版本化密钥，
   `cmd/rotate-key` 能做无停机轮换），vault-cloud 已经是那个专门存凭据的无网络 qube。
   再往 `$HOME/.qubes-air/keys` 写一份**明文**私钥，等于把凭据散到第三个地方，
   而那个地方没有加密、没有版本、没有审计。
2. **它把私钥写在「运行脚本的机器」上，而不是「用这把钥匙的机器」上。**
   agent 证书轮换（§7.1）早就不这么干了：密钥在要用它的那台机器生成，只有 CSR 过网。
   这是同一条原则，WireGuard 没有理由例外。
3. **轮换必须和对端协同**，换完公钥要推给网关。shell 脚本做不到，所以它只能打印一句
   「请手工分发」——而手工那一步一旦漏掉，隧道就断了，且**没有任何东西会报错**。

**该做成什么：照抄 `CertRenewer` 的形状。** console 发起，密钥在持有它的那一侧生成，
只有公钥出来，console 负责推到对端并记进注册表。区别只是对端是网关而不是 agent。

### 12.2 「单独一个 qube 存放」——对，但 WG 不能像 SSH 那样拆

vault-cloud 里已经有两种模式，而它们的安全性质**不一样**：

- `qubes.SshAgent`（split-ssh）：私钥**从不出来**，只出签名。
- `qubesair.GetCredential`：按名**把凭据发出去**。

**WireGuard 只能用第二种。** WG 的握手在内核里做，没有「签名预言机」模式——
私钥必须存在于**运行 wg0 的那个网络命名空间**里。所以「放在 vault-cloud」只能是
「vault-cloud 在启动时把它发给运行 wg0 的那一侧」，不可能是 split-ssh 那种「永不出库」。

这个差别值得写下来，因为很容易顺着 split-ssh 的直觉以为 WG 也能这么保护。**不能。**
能拿到的是：私钥不落在 console 的磁盘上、集中管理、可轮换、可审计——**不是**「私钥永不离开保险库」。

于是 wg0 该放哪就有了答案：**一个专门的 sys-vpn qube，除了跑 WireGuard 什么都不做**，
console 用它当 NetVM。

**这不是 §11.1 那个被否决的模式**，区别要点清楚：被否决的是 **relay 兼任网络网关**
（`sys-remote` 同时处理 qrexec 调用**和**给别人当 NetVM，控制平面和网络平面被揉在一起）。
一个只跑 WireGuard、不承载任何 qrexec 服务的 sys-vpn，是 Qubes 的标准做法，
控制面（console/relay）仍然是独立的 qube。**平面是分开的，只要那个 qube 不同时是 relay。**

### 12.3 组网确实要看云的能力——而且它决定了 console 要不要改代码

这是三点里影响最大的一条。各家能力不同，而且**分成两类**，代价差别很大：

**A 类：路由型（console 零改动）**
到 zone 私网段有一条真实路由，`agentprobe` 拨私网 IP 直接就通。
- 自建 WireGuard 网关：四种环境都能用，PVE 异地场景**只有这条路**
- GCP Cloud VPN / Cloud Router、AWS Site-to-Site VPN / Transit Gateway：托管版的同一件事

**B 类：按连接转发型（console 必须改）**
云厂商自己的「不开入站也能到达私有实例」原语，用 IAM 授权而不是共享密钥：
- **GCP: IAP TCP forwarding** —— 不需要网关实例、不需要公网 IP、不需要防火墙放行
- **AWS: SSM Session Manager port forwarding** —— 同样形状，靠实例上的 SSM agent

B 类在安全上更好（授权是 IAM，可审计、可撤销、没有长期共享密钥，也不用多养一台网关机），
但它**不是一条路由**：每次连接都要起一个转发通道。而 console 现在是
`net.JoinHostPort(qube.IPAddress, 8443)` 直接拨——那句话在 B 类下不成立。

所以 §11.4 那句「console 侧零改动」只对 **A 类**成立。要支持 B 类，需要把「怎么拨到这台 qube」
抽成一个 per-zone 的拨号器，`AgentProber` / `CertRenewer` / `AgentBootstrapper` 三处共用
（它们现在各自 `dial`，但都走同一个 `agentSession`，所以接缝是现成的）。

### 12.4 建议的顺序

> 注意这一节讲的是**网络可达性**（怎么拨到 qube），和 §4 讲的**身份投递**
> （怎么把 token 送进 qube）是两件事。(f) 在投递那条线上已否决，不影响这里。

1. **先决定 wg0 放哪**（sys-vpn qube），因为它决定 console 侧密钥归谁保管，
   也决定 §12.1 那个轮换服务往哪个 qube 发 qrexec。
2. **PVE 异地 + 自建网关先跑通 A 类**，它是唯一四种环境通用的形状，且 console 不用改。
3. **云上再评估 B 类**。如果决定上 IAP/SSM，先做 §12.3 那个 per-zone 拨号器抽象，
   否则三处 dial 会各长一套。
4. **轮换服务最后做**，它依赖 1 和 2 —— 没有网关就没有对端可推公钥。

### 12.5 A 和 B 能不能都支持：能，而且接缝只有两处

把整个拨号面数清楚之后，结论比 §12.3 那句「console 必须改」听起来乐观：

```
internal/transport/grpc/client.go:158   grpc.NewClient(cfg.RemoteEndpoint, creds)
internal/service/agentprobe.go:346      (&net.Dialer{}).DialContext(ctx, "tcp", addr)
```

**就这两处。** 三个服务侧的调用点（`certrenew.go:977`、`agentbootstrap.go:368`、
`agentprobe.go:379`）都是经 `transportgrpc.ClientConfig` 汇到第一处的，所以它们不是三份拨号，
是一份戴了三顶帽子。而 gRPC 早就有现成的钩子：
`grpc.WithContextDialer(func(ctx, addr) (net.Conn, error))`。

于是「同时支持 A 和 B」的改动是：

1. `ClientConfig` 加一个 `Dialer func(ctx, addr) (net.Conn, error)`；非空时传
   `grpc.WithContextDialer(...)`，为空时保持今天的行为。
2. `agentprobe.connect` 用同一个函数取代那句 `net.Dialer{}.DialContext`。
3. 按 zone 解析出用哪个 dialer。

#### 一个让事情小很多的简化：A 的所有变体是同一个实现

LAN 同网段、自建 WireGuard 网关、GCP Cloud VPN、AWS Site-to-Site / Transit Gateway——
从 console 看**全都一样**：到那个私网段有一条路由，`net.Dial` 就通。

所以 console 侧的选项**不是「A 还是 B」**，而是：

| zone 的可达方式 | dialer |
|---|---|
| `direct`（默认，覆盖 LAN / WireGuard / Cloud VPN / TGW） | 今天这个 |
| `gcp-iap` | IAP TCP forwarding |
| `aws-ssm` | SSM port forwarding |

**WireGuard 根本不出现在 console 代码里。** 它是让 `direct` 成立的基础设施，不是一个分支。
这也意味着 §11 那套和这里是正交的：先上 WG 让 GCP 通，将来再换 IAP，console 的 zone 配置
从 `direct` 改成 `gcp-iap`，别的都不动。

#### 代价的诚实版本

接缝很小，**两个 B 实现本身不小**：

- **GCP IAP**：认证用 `golang.org/x/oauth2/google` 拿 token，然后连
  `tunnel.cloudproxy.app` 的 WebSocket 中继协议。协议是公开的，但帧格式要自己实现；
  另一条路是 shell out `gcloud compute start-iap-tunnel`，那就又回到「奇怪的脚本」了（§12.1）。
- **AWS SSM**：官方路径是 `session-manager-plugin`，同样是 WebSocket 协议，
  自己实现的成本比 IAP 高，而 shell out 的问题一样。

还有两个**不能在抽象里丢掉**的性质：

- `agentprobe.connect` 现在**刻意区分**「没人监听」（dial 失败）和「握手被拒」（TLS 失败），
  §7.4 那类误诊就是靠这个避免的。所以 dialer 只负责给出 `net.Conn`，**TLS 仍留在外面做**。
- B 类下 `qube.IPAddress` **可能根本不是拨号目标**（IAP 按 project/zone/instance 寻址）。
  所以 dialer 的入参该是 `(zone, qube)` 而不是一个 `addr` 字符串——这一点如果第一版就按
  `addr string` 定死，等 IAP 来的时候要再改一遍。

#### 建议

**接缝现在就留，B 的实现按需再写。** 三处调用点今天恰好共用一条路径，所以现在抽是一次
纯重构、零行为变化；等有了第二个实现再抽，就得同时动探测、续期、bootstrap 三条
已经在真机上跑着的链路。

但**不要为了留接缝而把入参定成 `addr string`**——那正是将来要返工的地方。
