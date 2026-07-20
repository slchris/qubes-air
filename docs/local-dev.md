# 本地开发（docker compose）

在自己的机器上跑控制台的前后端，用来改 WebUI 和调 API —— 不需要 Qubes、不需要
PVE、不需要 Go/Node 工具链。

```bash
docker compose up          # 前后端起来，改代码热更新
docker compose up --build  # 改了 Dockerfile 或依赖之后
docker compose down        # 停止（保留 dev 数据库）
docker compose down -v     # 停止并丢掉 dev 数据库
```

然后打开 **<http://127.0.0.1:5173>**。

首屏是登录门，粘这个 token 进 Settings：

```
devtoken
```

## 端口

| 服务 | 宿主机 | 容器内 |
|---|---|---|
| 前端（vite，HMR） | 5173 | 5173 |
| 后端 API | **8098** | 8080 |

后端用 8098 是因为 8080 太常被占（这台机器上 OrbStack 就占着）。要改：

```bash
BACKEND_PORT=9000 FRONTEND_PORT=3000 docker compose up
```

## 这套环境是什么、不是什么

**是**开发环境。凭据是写死在 `docker-compose.yml` 里的一次性值（`devtoken` 和一个
32 字节的开发密钥），数据库是个 volume 里的 sqlite。搞砸了 `docker compose down -v`
就没了。

**不是**部署方式。真正的控制台是 Qubes 专属 AppVM 里的 systemd 服务，由
`qubes-salt-config` 的 `salt/qubesair` 部署，token 是生成的、密钥是真的。
**这里的凭据在 git 里，任何真实环境都不要复用。**

编排（`QUBES_AIR_ORCHESTRATOR_ENABLED`）默认**关**：开了它会 shell 到 terraform
去操作真集群。要开就明确地开，并且用你确实打算用的那份凭据。

## 常见问题

**改了后端代码没生效** —— 后端是 `go run`，重启容器才会重新编译：

```bash
docker compose restart backend
```

前端不用管，vite 有 HMR。

**第一次 `up` 很慢** —— 后端在容器里编译（含 cgo/sqlite），几分钟正常。健康检查
的 `start_period` 给了 180 秒就是为这个；模块和构建缓存放在具名 volume 里，之后
会快很多。

**页面一片报错** —— 多半是没贴 token。没有 token 时每个 `/api/v1` 都是 401，
登录门会接住并告诉你去哪拿。

**`node_modules` 相关的怪错** —— 容器里的 `node_modules` 是独立 volume，不是宿主机
那份（宿主机上是 macOS/arm64 的二进制，在容器里跑不了）。依赖变了就
`docker compose up --build`。

## 和真机的关系

在这里改好 WebUI 之后，走正常发布路径上机器：打 tag → CI 产出
`qubes-air-console-web.tar.gz` 和二进制 → 在 `salt/config.jinja` 里钉版本和
SHA256 → 应用 `qubesair.console`。见 [`salt/qubesair/README.md`](https://github.com/slchris/qubes-salt-config/blob/main/salt/qubesair/README.md)。
