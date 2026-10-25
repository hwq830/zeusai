# Zeus 上 K8s（阿里云 ACK）

`deploy/helm/zeus` 部署 api / orchestrator / admin 三个进程以及数据库迁移；镜像由 `deploy/docker/Dockerfile` 构建，控制台镜像在 hwq830/zeusai-admin。

```
            ┌──────── Ingress（api.zeus…，SSE 不缓冲、1h 超时）
App ───────▶│ api ×2–10（HPA）─┐
            └──────────────────┤        RDS PostgreSQL 16 + pgvector
控制台(内网) ─▶ admin ×1 ────────┼──────▶ Redis / Tair
                               │        Temporal（集群内 Helm 或自建）
              orchestrator ×2 ─┘        NAS（RWX，PDF/截图，OSS 接入前）
```

## 镜像

| 镜像 | 内容 | 构建 |
| --- | --- | --- |
| `zeus-server` | api、admin、zeusctl（distroless，非 root） | `docker build -f deploy/docker/Dockerfile --target server .` |
| `zeus-orchestrator` | orchestrator + Chromium + 中文字体（PDF） | `--target orchestrator` |
| `zeus-admin-console` | 控制台静态文件（nginx） | zeusai-admin 仓库 `docker build .` |

提示词、简报模板、迁移、报告模板都编进了二进制，镜像里不需要仓库文件。CI（`.github/workflows/deploy.yml`）在 PR 上构建镜像并检查 chart；设置仓库变量 `ACR_REGISTRY`（如 `registry.cn-hangzhou.aliyuncs.com/zeus`）和 secrets `ACR_USERNAME` / `ACR_PASSWORD` 后，main 推 `:main` 和 `:sha-xxxx`，`v1.2.3` tag 推 `:1.2.3`。国内机器自己构建时，基础镜像可用 `--build-arg GO_IMAGE=… RUNTIME_IMAGE=… CHROMIUM_IMAGE=…` 换成 ACR 里的副本，Go 依赖加 `--build-arg GOPROXY=https://goproxy.cn,direct`。

## 第一次部署

前提：ACK 集群 ≥ 1.30，装好 nginx-ingress（或改 `api.ingress` 用 ALB）、NAS CSI（`blob.storageClass`），能拉 ACR 镜像。

1. **外部依赖**
   - RDS PostgreSQL 16，开启 `vector` 插件（`create extension vector;` 需要高权限账号先执行一次）。
   - Redis（Tair 或社区版均可）。
   - Temporal：MVP 用官方 chart 装在集群里（`temporal` 命名空间，持久化指向 RDS 上单独的库），前端地址填 `config.ZEUS_TEMPORAL_HOST`。
2. **密钥**（chart 不写密钥；生产用 External Secrets 从 KMS 同步，先手工也行）：
   ```sh
   kubectl create ns zeus
   kubectl -n zeus create secret generic zeus-secrets \
     --from-literal=ZEUS_DATABASE_URL='postgres://zeus:***@pgm-xxx.pg.rds.aliyuncs.com:5432/zeus?sslmode=require' \
     --from-literal=ZEUS_REDIS_URL='redis://:***@r-xxx.redis.rds.aliyuncs.com:6379/0' \
     --from-literal=ZEUS_JWT_SECRET="$(openssl rand -hex 32)" \
     --from-literal=ZEUS_ADMIN_JWT_SECRET="$(openssl rand -hex 32)" \
     --from-literal=ZEUS_FILES_SECRET="$(openssl rand -hex 32)" \
     --from-literal=ZEUS_QWEN_API_KEY=*** --from-literal=ZEUS_BOCHA_API_KEY=***
   # 可选：推送
   kubectl -n zeus create secret generic zeus-apns --from-file=apns.p8=AuthKey_XXXX.p8
   kubectl -n zeus create secret docker-registry acr --docker-server=registry.cn-hangzhou.aliyuncs.com --docker-username=… --docker-password=…
   ```
   其他可选项（短信、微信、APNs Key ID / Team ID、高德）也放进 `zeus-secrets`，变量名见 `server/README.md`。
3. **values**：复制一份，例如 `values-prod.yaml`：
   ```yaml
   image:
     registry: registry.cn-hangzhou.aliyuncs.com/zeus
     tag: "1.0.0"
   imagePullSecrets: [{ name: acr }]
   publicBaseURL: https://api.<正式域名>
   apnsKeySecret: zeus-apns
   api:
     ingress: { host: api.<正式域名>, tlsSecret: zeus-api-tls }
   ```
   `config` 里放非密钥的 `ZEUS_*` 变量；`modelgw`、`moderationKeywords` 是挂进容器的文件内容。
4. **安装**：
   ```sh
   helm upgrade --install zeus deploy/helm/zeus -n zeus -f values-prod.yaml
   ```
   每次 install / upgrade 前，chart 先跑一个 Job：`zeusctl migrate` 后 `zeusctl seed`，成功后才滚动更新。
5. **第一个控制台账号**（NOTES 里也有）：
   ```sh
   kubectl -n zeus exec deploy/zeus-admin -- env ZEUS_ADMIN_PASSWORD='至少10位' /usr/local/bin/zeusctl admin-create you@example.com owner
   kubectl -n zeus port-forward svc/zeus-admin 8081   # 打开 http://localhost:8081
   ```
   admin 默认不建 Ingress；要开就配内网 SLB 或 IP 白名单（`admin.ingress.annotations`）。

## 发版 / 回滚

```sh
git tag v1.0.1 && git push origin v1.0.1          # CI 推镜像 :1.0.1
helm upgrade zeus deploy/helm/zeus -n zeus -f values-prod.yaml --set image.tag=1.0.1
helm rollback zeus -n zeus                         # 回滚 Deployment；数据库迁移不回滚，迁移要向前兼容
```

## 设计取舍

- **PG、Redis、Temporal 不在 chart 里**：生产用托管服务，chart 只管无状态进程。staging 可以用同一个 chart，换 values。
- **迁移用 Helm hook Job**，不放进 api 的 initContainer，避免多副本并发迁移。
- **共享目录用 NAS（RWX）**：PDF 由 orchestrator 写、api 签名下发、admin 读反馈截图，三个进程必须看到同一份文件。OSS 接入后关掉 `blob.enabled`。
- **orchestrator 的 Chromium 关掉自带沙箱**（`chromiumNoSandbox`）：pod 拿不到它需要的权限；以后报告浏览进 Kata 沙箱后再收紧。
- **安全**：所有容器非 root（65532）、drop ALL capabilities、seccomp RuntimeDefault，除 orchestrator 外根文件系统只读，ServiceAccount 不挂 token。
- **SSE**：Ingress 默认关闭缓冲、读超时 1 小时；api 有 5 秒 preStop，滚动更新断开的流可以带 Last-Event-ID 重连续上。

## 已验证 / 未验证

- `helm lint`、`helm template` + kubeconform（K8s 1.31 schema）通过。
- 用 chart 渲染出的环境变量、挂载文件、非 root、只读根文件系统运行 `zeus-server` / `zeus-orchestrator` 镜像：migrate、seed、admin-create 和 `scripts/smoke.sh` 全流程通过。控制台镜像的两种用法（复制进 admin、nginx 单独运行）都验证过。
- 云端开发容器里起不了真 K8s（kind / k3s 的 pod sandbox 都被宿主限制挡住），所以 Ingress、HPA、PDB、NAS 挂载没有在真集群上跑过。orchestrator 镜像里的 apt 安装 Chromium 也没在本地构建成功（网络限制），由 CI 构建和验证 PDF 打印。第一次上 ACK 时建议先在 staging 走一遍。
