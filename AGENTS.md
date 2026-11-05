# AGENTS.md

给在这个仓库工作的编码代理（Codex、Claude 等）看的说明。人读的文档见 `README.md`、`server/README.md`、`deploy/cloud-dev/README.md`。

Zeus（暂定名）个人 AI 助理的服务端：Go 1.25，单模块；接口契约 `api/openapi.yaml`（App）和 `api/admin.yaml`（管理后台）；迁移在 `server/migrations`（goose）；查询用 sqlc。相邻仓库：`zeusai-admin`（管理后台前端）、`zeusai-ios`（iOS App）。Android 暂停开发。

和 Jason 交流用中文。

## 改代码

- 不要直接推 main。每项改动开一个分支和一个 PR，等 Jason 合并。
- 提交前跑：`go build ./... && go vet ./... && go test ./...`。数据库测试需要 `ZEUS_TEST_DATABASE_URL`，接口端到端测试还要 `ZEUS_TEST_REDIS_URL` 和 `ZEUS_TEST_TEMPORAL_HOST`；本地没有这些服务时测试会跳过，以 CI 结果为准。
- 改了 `api/openapi.yaml`：通知 iOS 跑 `scripts/sync-openapi.sh`。改了 `api/admin.yaml`：在 zeusai-admin 跑 `npm run sync:api`。
- 改了 `web/report-template/` 的 DOM、CSS 或主题变量：通知 iOS 重新生成 Mock 快照，并核对 `zeusai-admin` 的 `src/lib/reportPreview.ts`——那里注入的 `:root` 是模板主题变量的副本，模板新增变量时要同步补上，否则后台预览的配色会和 App / 分享页不一致。
- 新迁移按编号顺序加在 `server/migrations`，不要修改已经部署过的迁移。
- `.env`、`secrets/`、任何 API Key、密码都不能进仓库、PR 描述或对话。

## 部署到云端调试环境（cloud-dev）

只在 Jason 明确要求部署时执行。这是联调环境，不是生产；生产形态见 `deploy/helm`，目前还没有生产环境。

### 环境

| 项 | 值 |
| --- | --- |
| 服务器 | 阿里云 ECS 香港，`ssh huwq@47.83.140.118`，root 用 `sudo su -` |
| 代码目录 | `/opt/zeus/zeusai`、`/opt/zeus/zeusai-admin`（两个仓库必须在同一目录下，compose 从 `../../../zeusai-admin` 构建控制台） |
| 编排 | `/opt/zeus/zeusai/deploy/cloud-dev`，`docker compose`（postgres+pgvector、redis、temporal、migrate、seed、api、orchestrator、admin、console、caddy） |
| 域名 | App 接口 `https://api-dev.huhuhome.top`，管理后台 `https://admin-dev.huhuhome.top`（只放行 `.env` 里 `ZEUS_ADMIN_ALLOW_IPS` 的出口 IP） |
| 本机 | Jason 的 Mac，仓库在 `~/claude/zeusai`、`~/claude/zeusai-admin`、`~/claude/zeusai-ios` |
| 控制台账号 | `hwq830@gmail.com`，密码在 Mac 桌面的密码文件和 ECS 上 `~/zeus-admin-password.txt`。只在命令里读取，不要打印出来 |

服务器上没有 GitHub 凭证，代码从 Mac 用 `git archive` 打包后经 ssh 解压过去。

### 部署前

1. **确认没有别的部署在进行。** 其他代理（例如 Claude 的「后端开发」讨论串）也会部署这台机器。检查锁文件：
   ```sh
   ssh huwq@47.83.140.118 'cat /opt/zeus/DEPLOY_LOCK 2>/dev/null || echo free'
   ```
   不是 `free` 就停下来告诉 Jason 谁在部署。空闲时写入锁（部署结束后删除）：
   ```sh
   ssh huwq@47.83.140.118 "echo \"codex $(date -u +%FT%TZ) <要部署的内容>\" | sudo tee /opt/zeus/DEPLOY_LOCK"
   ```
2. **更新本机代码到 main：**
   ```sh
   cd ~/claude/zeusai && git fetch && git status --short && git checkout main && git merge --ff-only origin/main
   cd ~/claude/zeusai-admin && git fetch && git status --short && git checkout main && git merge --ff-only origin/main
   ```
   本地有未提交或未推送的改动就停下来报告，不要 `reset --hard` 掉别人的工作。记下两个仓库的 HEAD commit。
3. **备份**（标记用当天日期加字母，如 `20261102d`，不要和已有的重复）：
   ```sh
   T=20261102d
   ssh huwq@47.83.140.118 "sudo bash -c '
     cd /opt/zeus/zeusai/deploy/cloud-dev &&
     docker compose exec -T postgres pg_dump -U zeus zeus > /opt/zeus/backup-$T.sql &&
     for i in zeus-server zeus-orchestrator zeus-admin-console; do docker tag \$i:local \$i:rollback-$T; done &&
     mkdir -p /opt/zeus/cfg-backup-$T && cp -a .env secrets /opt/zeus/cfg-backup-$T/ &&
     ls -lh /opt/zeus/backup-$T.sql'"
   ```
   备份文件大小为 0 或命令失败，就不要继续。

### 部署

4. **推送代码。** 已知坑：`git archive | tar -x` 直接解压到原目录不会删除 git 里已经删掉的文件，残留的旧文件曾经把控制台构建弄坏。所以先解压到干净的临时目录，再用 `rsync --delete` 同步过去，同时保留 `.env` 和 `secrets/`。不要整个目录 `mv` 替换：容器里绑定挂载着 `secrets/`、`Caddyfile` 等文件，换目录会让没重建的容器指向旧路径。
   ```sh
   for r in zeusai zeusai-admin; do
     (cd ~/claude/$r && git archive --format=tar HEAD) | ssh huwq@47.83.140.118 "sudo bash -c '
       rm -rf /opt/zeus/$r.new && mkdir -p /opt/zeus/$r.new && tar -x -C /opt/zeus/$r.new &&
       rsync -a --delete --exclude=/deploy/cloud-dev/.env --exclude=/deploy/cloud-dev/secrets/ --exclude=/node_modules/ \
         /opt/zeus/$r.new/ /opt/zeus/$r/ && rm -rf /opt/zeus/$r.new'"
   done
   ```
   先加 `--dry-run -i` 跑一次，看清会删哪些文件再正式执行。

5. **重建并启动：**
   ```sh
   ssh huwq@47.83.140.118 "sudo bash -c 'cd /opt/zeus/zeusai/deploy/cloud-dev && docker compose up -d --build && docker compose ps -a'"
   ```
   期望：`migrate`、`seed`、`console`、`volumes-init` 为 `Exited (0)`，其余 `running`。
6. **确认迁移版本** 等于 `server/migrations` 里最大的编号：
   ```sh
   ssh huwq@47.83.140.118 "sudo bash -c 'cd /opt/zeus/zeusai/deploy/cloud-dev && docker compose exec -T postgres psql -U zeus -tAc \"select max(version_id) from goose_db_version\"'"
   ```
   有针对这次改动的数据校验（PR 描述里会写），一并用 psql 查。

### 部署后

7. **冒烟测试**（在 Mac 的 `~/claude/zeusai` 下跑），期望全部通过（目前 22 项）：
   ```sh
   ZEUS_SMOKE_TIMEOUT=300 ZEUS_API=https://api-dev.huhuhome.top ZEUS_ADMIN_API=https://admin-dev.huhuhome.top \
   ZEUS_SMOKE_ADMIN_EMAIL=hwq830@gmail.com ZEUS_SMOKE_ADMIN_PASSWORD="$(cat <桌面密码文件>)" scripts/smoke.sh
   ```
   控制台检查需要从白名单 IP 访问；Mac 的 VPN 出口变了会返回 403，告诉 Jason 去改 `ZEUS_ADMIN_ALLOW_IPS`，不要自己放开。
8. **看日志**：`docker compose logs --since 10m api orchestrator admin | grep -iE 'error|panic'`。
9. **记录并解锁：**
   ```sh
   ssh huwq@47.83.140.118 "echo \"$(date -u +%FT%TZ) codex zeusai=<commit> admin=<commit> goose=<版本> smoke=<结果> backup=$T\" | sudo tee -a /opt/zeus/DEPLOY_LOG && sudo rm -f /opt/zeus/DEPLOY_LOCK"
   ```
10. **向 Jason 汇报**：部署的两个 commit、迁移版本、校验和冒烟结果、备份标记、回滚方式。

### 回滚

```sh
cd /opt/zeus/zeusai/deploy/cloud-dev
for i in zeus-server zeus-orchestrator zeus-admin-console; do docker tag $i:rollback-$T $i:local; done
docker compose up -d --no-build
```

代码目录用上一个 commit 按第 4 步重新推送即可。新迁移改坏了数据才需要恢复数据库：先停 api、orchestrator、admin，再用 `backup-$T.sql` 恢复。恢复数据库会丢掉部署之后产生的数据，必须先问 Jason。

### 绝对不要

- `docker compose down -v`、`docker volume rm`、`docker system prune --volumes`：数据都在卷里（`pgdata` 等）。
- 修改 `.env` 里的密钥，尤其是 `ZEUS_SECRETS_KEY`（改了以后后台保存的所有 API Key 都解不开）和各个 JWT secret（改了所有人掉登录）。
- 改安全组、DNS、`ZEUS_ADMIN_ALLOW_IPS`，或者把 5432/6379/7233/8080/8081 暴露到公网。
- 在服务器上留下 GitHub token。
- ssh 写入被 Mac 的权限检查拦住时想办法绕过。如实告诉 Jason，等他批准。
- 两个代理同时部署。
