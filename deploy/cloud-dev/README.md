# 阿里云云端调试环境（单台 ECS + docker compose）

一台 ECS 上跑全套：PostgreSQL + pgvector、Redis、Temporal、api、orchestrator、admin + 控制台，以及 Caddy（自动 HTTPS）。这是联调用的环境，不是生产形态（生产见 `deploy/helm`）。

```
iPhone ──HTTPS──▶ Caddy :443 ─▶ api :8080 ─┐
浏览器（白名单 IP）─▶ Caddy ─▶ admin :8081 ─┼─▶ postgres / redis / temporal（都在这台机器上）
                              orchestrator ┘   数据在 docker 卷里
```

## 1. 开机器（约 10 分钟）

- **地域**：推荐**中国香港**，域名不需要 ICP 备案，马上就能用 HTTPS。选境内地域的话，备案下来之前 80/443 不能用域名访问。
- **规格**：4 vCPU / 8 GiB（如 ecs.g7.xlarge 或 u1 通用型），系统盘 ESSD 60 GiB，镜像选 Ubuntu 24.04 或 Alibaba Cloud Linux 3。
- **公网**：分配公网 IP（按流量计费即可）。
- **安全组**：入方向放行 22（只放你的 IP）、80、443。不要放行 5432/6379/7233/8080/8081。
- **域名解析**：两条 A 记录指向公网 IP，例如 `api-dev.<你的域名>`、`admin-dev.<你的域名>`。

## 2. 装环境（约 10 分钟）

```sh
ssh root@<公网IP>
curl -fsSL https://get.docker.com | sh          # 境内机器用阿里云的 docker-ce 源
mkdir -p /opt/zeus && cd /opt/zeus
# 两个私有仓库：在 GitHub 建一个只读 fine-grained token（Contents: Read，仓库选 zeusai、zeusai-admin）
git clone https://<token>@github.com/hwq830/zeusai.git
git clone https://<token>@github.com/hwq830/zeusai-admin.git
```

两个仓库必须放在同一个目录下（compose 从 `../../../zeusai-admin` 构建控制台）。

## 3. 配置并启动（首次构建约 5–10 分钟）

```sh
cd /opt/zeus/zeusai/deploy/cloud-dev
cp env.example .env
for k in ZEUS_PG_PASSWORD ZEUS_JWT_SECRET ZEUS_ADMIN_JWT_SECRET ZEUS_FILES_SECRET; do
  sed -i "s/^$k=$/$k=$(openssl rand -hex 32)/" .env
done
vi .env        # 填两个域名、ZEUS_ADMIN_ALLOW_IPS（curl ifconfig.me 查你的出口 IP）
docker compose up -d --build
docker compose ps -a     # migrate / seed / console / volumes-init 是 Exited (0)，其余 running
```

启动顺序由 compose 保证：卷权限 → 迁移 → 种子数据 → api / orchestrator / admin → Caddy 申请证书（第一次要 1 分钟左右）。

创建控制台账号（不设 `ZEUS_ADMIN_PASSWORD` 时会打印一次随机密码）：

```sh
docker compose exec admin /usr/local/bin/zeusctl admin-create you@example.com owner
```

验证（在你自己的电脑上，zeusai 仓库根目录）：

```sh
ZEUS_API=https://api-dev.<域名> ZEUS_ADMIN_API=https://admin-dev.<域名> \
ZEUS_SMOKE_ADMIN_EMAIL=you@example.com ZEUS_SMOKE_ADMIN_PASSWORD=<密码> scripts/smoke.sh
```

## 4. App 连过来

在 `zeusai-ios/App/Local.xcconfig` 里加两行，然后执行 `xcodegen generate`，scheme 选「Zeus Dev」。模拟器和真机都能直接连：

```
ZEUS_DEV_SERVER = api-dev.<域名>
ZEUS_DEV_SCHEME = https
```

`ZEUS_ENV=dev` 时短信验证码固定为 `ZEUS_DEV_SMS_CODE`。这个环境在公网上，验证码不要外传；接上真短信以后改成 `ZEUS_ENV=staging`。

## 5. 日常操作

```sh
cd /opt/zeus/zeusai && git pull && cd ../zeusai-admin && git pull
cd /opt/zeus/zeusai/deploy/cloud-dev && docker compose up -d --build     # 更新代码，迁移自动执行
docker compose logs -f api orchestrator                                  # 看日志
ssh -L 8233:localhost:8233 root@<公网IP>                                  # 本机打开 http://localhost:8233 看 Temporal
docker compose exec postgres psql -U zeus                                # 进数据库
```

接真模型 / 搜索 / 推送：确认 `.env` 里有 `ZEUS_SECRETS_KEY`，在控制台“API Key”页填千问和博查的 Key 并测试通过，再在 `.env` 里改 `ZEUS_LLM_PROVIDER=gateway`、`ZEUS_SEARCH_PROVIDER=bocha`（也可以直接在 `.env` 里填 key）；推送把 `.p8` 放到 `secrets/apns.p8`，取消 `.env` 里 APNs 那几行的注释，然后 `docker compose up -d`。

备份：数据都在 docker 卷 `zeus_pgdata` 里，调试环境建议开 ECS 自动快照。

## 已验证

在云端开发容器里用 `api.localhost` / `admin.localhost`（Caddy 本地证书）跑通了整套：`docker compose up`、迁移、种子数据、admin-create、经 Caddy HTTPS 跑 `scripts/smoke.sh` 全部通过，控制台页面能打开，非白名单 IP 访问控制台返回 403。Let's Encrypt 申请真证书要等真实域名解析好以后才能验证。
