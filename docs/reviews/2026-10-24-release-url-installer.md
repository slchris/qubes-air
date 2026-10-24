# 正式 release URL 与干净 guest 的 agent 安装

首次使用 v0.2.0 的 GitHub release URL 置备时，guest 没有安装 agent，cloud-init 留下
SHA256 不匹配错误。生成的安装脚本调用 curl 时未跟随重定向，而正式下载地址会跳转到
制品服务；现有测试把 curl 替换成直接复制文件，未覆盖这条路径。

另一个安装前置问题是干净 guest 缺少 `python3-gi`、`gir1.2-glib-2.0` 和 `x11-utils`，
它们已经是 agent 包声明的依赖。原来的 `dpkg -i` 不解析这些依赖；修复下载并不足以让
该 guest 正常安装。

## 行为与边界

- 继续使用 cloud-init 已明确安装的 curl，去除语义不同的 wget 后备路径；curl 缺失时失败。
- 初始地址仍只使用 HTTP/HTTPS，重定向只接受 HTTPS，最多跟随 5 次。TLS 验证保持开启，
  不转发跨主机凭据；显式禁用 curl 配置文件，避免镜像中的配置改变下载行为。
- 保留每次传输 120 秒上限，增加 15 秒连接上限和 180 秒重试窗口；curl 的单次传输可能
  延续到重试窗口之后，二者不能直接当成 180 秒总时限。语义见
  [curl 官方手册](https://curl.se/docs/manpage.html#--retry-max-time)。
- 取得完整包后仍先验证固定 SHA256，任何 HTTP 成功、跳转或有效 TLS 都不代替摘要检查。
- 验证后的固定本地包交给 APT，以非交互方式安装必需依赖、不安装 recommends。仓库信任
  仍由 guest 的 APT 配置决定；未添加仓库、信任钥或放宽包认证。
- 安装有 300 秒预算，终止后 10 秒仍不退出则结束进程组；锁等待与 HTTP/HTTPS 读取有各自
  30 秒上限。失败保留诊断和 marker，移除下载文件，不继续显式启动 agent。APT 可能已经
  解包或配置部分依赖，失败不宣称回滚这些包。

本阶段不更改 bootstrap token、CA、agent 的 mTLS 身份或桌面 opt-in 约束。生产已有
Console/Relay 的 v0.2.0 部署不构成修复后置备验收。

## 回归与验收

先用本机真实 TLS 服务和实际 curl 复现失败：合法 302 未访问制品端点，循环只发出第一次
请求；缺依赖时仍使用 dpkg 并失败。修复后的测试覆盖合法 HTTPS 跳转、跳转后摘要错误、
HTTPS 降级拒绝、循环上限、错误 CA、curl 缺失、依赖解析与安装失败。包安装和 systemd
使用替身，失败 marker、下载清理和“不能继续启动”的边界均按运行结果断言。
测试路径全部落在临时目录，包括模拟串口输出，不写宿主机 `/dev/console`。

定向 Go race 回归通过。另在干净的 Debian 13 Linux/amd64 容器中，仅预装 curl 与
CA，并先确认上述三个依赖不存在，再执行当前生成的安装器。实际从 GitHub 的正式
`v0.2.0` URL 下载、校验固定 SHA256、由 APT 安装全部声明依赖和 agent 均成功，
agent 的 `--version` 为 `0.2.0`，没有遗留失败 marker 或下载文件。
容器没有 systemd PID 1，服务命令使用替身；该结果只证明实际下载与依赖安装，不能
代替 bootstrap、服务就绪或生产生命周期验收。

`make pre-commit audit` 全部通过，Go race 总语句覆盖率为 75.6%，前端为 0 error、
0 warning；完整 lint、安全、依赖、ShellCheck、YAML、文档和入口冒烟门禁通过。
修复制品的正式发布、部署和生产生命周期验收仍待完成。
