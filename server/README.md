# Zeus 服务端

一个 Go module（仓库根目录 `go.mod`），编译出多个进程：

| 进程 | 入口 | 状态 |
| --- | --- | --- |
| `api` | `server/cmd/api` | 登录、额度、模板、定时简报、今日简报、任务与 SSE、连接器、记忆 |
| `orchestrator` | `server/cmd/orchestrator` | Temporal worker：每日简报工作流 |
| `admin` | `server/cmd/admin` | 管理员控制台接口 `/admin/v1`（契约 `api/admin.yaml`，前端在 hwq830/zeusai-admin），只在内网暴露 |
| `zeusctl` | `server/cmd/zeusctl` | `migrate` 迁移数据库，`seed` 导入官方模板、连接器目录和内置配置项，`admin-create` 创建控制台账号 |
| `modelgw` / `sandboxmgr` / `egress` / `sandboxd` | 待写 | 第 3–4 阶段 |

## 目录

```
server/
├── cmd/                     # 各进程 main
├── migrations/              # goose SQL 迁移（嵌入二进制）
└── internal/
    ├── httpapi/             # 实现 api/openapi.yaml 生成的 strict server；gen/ 为生成代码
    ├── adminapi/            # 实现 api/admin.yaml：控制台接口、角色权限、审计日志
    ├── db/                  # sqlc：queries/*.sql → Go；所有用户数据查询都带 user_id
    ├── auth/  quota/  schedule/  template/  briefing/  task/  connector/
    ├── events/              # 任务事件：Redis Stream，每任务一条，序号即 SSE id
    ├── workflows/           # Temporal 工作流与活动；也实现 orch.Client 给 api 用
    ├── orch/                # api 与 orchestrator 之间的接口
    ├── llm/  search/  push/ # 模型、搜索、推送的接口与 mock 实现
    ├── notify/              # 消息中心：先写站内消息，再按偏好、免打扰、频控决定是否推送；运营推送活动
    └── platform/            # 配置、数据库连接、测试工具
```

提示词和官方简报模板在仓库根目录 `prompts/`，是场景配置的唯一来源；每个任务记录所用的 `prompts.Version`。

## 本地运行

一条命令起全套（依赖、迁移、种子数据、控制台账号、api / orchestrator / admin），再跑一遍冒烟：

```sh
scripts/dev-up.sh          # 依赖已在本机端口上就直接用，否则 docker compose 起 deploy/local/compose.yaml
scripts/smoke.sh           # 登录 → 简报 → SSE → 报告 → 分享 → 消息 → 反馈；设 ZEUS_SMOKE_ADMIN_* 再查控制台
scripts/dev-down.sh        # 停进程；--deps 连依赖一起停
```

国内拉不到 Docker Hub 时设 `ZEUS_DOCKER_MIRROR`（镜像加速域名），或按 `scripts/dev-up.sh` 开头的说明用 Homebrew 装依赖；Go 依赖可设 `GOPROXY=https://goproxy.cn,direct`。

手动方式：

```sh
docker compose -f deploy/local/compose.yaml up -d --wait
go run ./server/cmd/zeusctl migrate
go run ./server/cmd/zeusctl seed
ZEUS_DEV_SMS_CODE=246810 go run ./server/cmd/api &
go run ./server/cmd/orchestrator &
```

管理员控制台：

```sh
ZEUS_ADMIN_PASSWORD=至少10位的密码 go run ./server/cmd/zeusctl admin-create you@example.com owner
go run ./server/cmd/admin &            # :8081，ZEUS_ADMIN_HTTP_ADDR 可改
```

| 设置 | 说明 |
| --- | --- |
| `ZEUS_ADMIN_JWT_SECRET` | 控制台令牌密钥，与 `ZEUS_JWT_SECRET` 分开；非 dev 必填 |
| `ZEUS_ADMIN_TOKEN_TTL` | 登录有效期，默认 `12h` |
| `ZEUS_ADMIN_STATIC_DIR` | 可选：zeusai-admin 构建出的 `dist/`，由 admin 进程一起提供 |
| `ZEUS_MODELGW_CONFIG`、`ZEUS_LLM_PROVIDER`、`ZEUS_SEARCH_PROVIDER` | 与 api 相同；路由文件只在数据库还没有路由时导入一次 |
| `ZEUS_SECRETS_KEY` | 加密控制台保存的模型 / 搜索 API Key（`openssl rand -hex 32`，至少 32 位）；api、orchestrator、admin 三个进程必须相同。不设置则不能在控制台保存 Key |

角色：`owner` 全部权限；`operator` 用户、额度、模板、连接器、任务、用户反馈；`moderator` 内容审核、停用用户、用户反馈、查看用户内容；`viewer` 只读（看不到报告和简报正文）。所有写操作进 `admin_audit_log`。在控制台改过的官方模板和连接器标记为 `admin_managed`，之后 `zeusctl seed` 不再覆盖它们的运营字段。控制台添加的屏蔽词与 `ZEUS_MODERATION_KEYWORDS_FILE` 合并，各进程约 1 分钟内生效，拦截记录写入 `moderation_events`。

用户内容：控制台“内容”分组的「调研报告」「简报」「内容质量」页（`/admin/v1/reports`、`/subscriptions`、`/briefings`、`/content/*`）。看正文要 `content.read`（owner、operator、moderator），每次查看记 `content.view` 审计；简报里模板标为 `private` 的区块默认打码，带 `revealReason` 才显示并另记 `content.reveal_private`。重新生成报告会新建一份（`reports.regenerated_from` 指向原报告）；重新生成简报只能是订阅时区里今天这期，原地覆盖前把旧版本存进 `briefing_revisions`。后台发起的任务记 `tasks.admin_id`，它们的模型调用不计入用户额度。退还额度是一条带 `ref`（`report:<id>` / `briefing:<id>`）的 `quota_grants`，每份只能退一次。下架（`hidden_at`）后 App 接口返回 `takenDown` 占位，HTML / PDF / 追问 / 新建分享返回 410 `taken_down`，已有分享全部撤销，并给用户发一条只进消息中心、不推送的通知；隐藏单条是把条目从 `sections` 移到 `briefing_hidden_items`，可放回。异常队列由报告和简报实时算出（失败、空简报、来源少于 3、不满意、“一般”评价、单份报告成本超过 ¥3），处理记录在 `content_issue_resolutions`。

运营数据：控制台“数据”分组（`/admin/v1/analytics/*`）只用服务端已有的表实时汇总。活跃 = 当天 App 带登录态调用过接口（api 进程每人每 10 分钟最多写一次 `user_daily_activity`，按北京时间计日，记录 `X-Zeus-Platform` / `X-Zeus-App-Version`），定时简报不算；迁移 00011 用注册日、主动发起任务的日子和最后在线日回填了历史。平台筛选按用户最近使用的平台（视图 `user_platform`）。

API Key：owner 在控制台“API Key”页为路由文件里的每个模型服务商和博查搜索保存 Key（AES-256-GCM 加密存 `provider_keys`，只显示末 4 位），可先测试再保存。每个服务商最多一把启用；启用的 Key 优先于环境变量，各进程约 1 分钟内生效、不用重启；停用后回退到环境变量。网关路由里没有 Key 的服务商会被跳过。保存 Key 时可以同时填 API 地址（如百炼业务空间专属地址，存 `provider_keys.base_url`），这把 Key 启用时调用和测试都走这个地址，不填用路由文件的 `base_url`（博查用默认地址）；地址可以在控制台随时修改。换 `ZEUS_SECRETS_KEY` 会让已保存的 Key 无法解密，需要重新添加。

模型路由：路由存在数据库里（`gateway_settings` 存各档位的调用顺序和熔断，`model_overrides` 存各环节的指定），owner 在控制台“模型路由”页修改，各进程约 1 分钟内生效。`ZEUS_MODELGW_CONFIG` 指向的路由文件只在数据库还没有路由时导入一次（api、orchestrator、admin 谁先启动谁导入）：文件里的服务商进 `model_providers`（保留 `api_key_env` 作为 Key 的兜底），价格进“模型定价”的手动价格，路由连同旧“模型指定”页的档位指定一起成为各档位的顺序；之后改文件不再生效。每档是一串依次尝试的服务商，每个可以选模型，不选用服务商这一档的默认模型；环节的指定先试，失败后按所在档位的顺序继续。每次修改记一个历史版本（`routing_versions`），可以一键恢复。保存前可以用服务商当前的 Key 试调一次，也可以实时拉一次可用模型（`GET /models`）。额度折算权重另在配置中心 `quota.token_weights` 里配。

模型定价：控制台“模型定价”页管理百炼模型的价格（元 / 百万 token），存 `model_prices`。没同步过时用服务端内置目录（`internal/modelgw/catalog_bailian.json`）；点“从百炼同步”会调用百炼控制台和 `bl model list` 用的模型列表接口（无需鉴权，非公开文档接口，失败时原价格不变）拉全部文本模型和标价。分档计价的模型（如 qwen-plus 按输入长度分档）计费按第一档。手动改的价格优先，之后同步不会覆盖，可随时恢复成同步价。计费顺序：手动价格（任何服务商）→ 百炼同步价（仅百炼地址的服务商）→ 内置目录。价格只在“模型定价”页设置，选模型时不再单独填价；没有价格的模型在“模型路由”页标为“无价格”，成本按 0 计。

服务商：控制台“服务商”页（原“API Key”页）按种类列出模型服务商和搜索服务商，管理各自的 Key。owner 可以新增模型服务商（任意 OpenAI 兼容接口：标识、显示名称、API 地址、各档默认模型、超时，可同时保存 Key），存 `model_providers`，各进程约 1 分钟内加载，不用改路由文件或重启。从路由文件导入的服务商和后台新增的一样可以修改、停用、删除。服务商放进“模型路由”的档位顺序或环节指定后才会被调用；还在被用时不能停用或删除，删除会一并删除它的 Key。

### 配置中心

远程配置（功能开关、灰度、熔断、文案、额度、最低版本）在 `server/internal/remoteconfig`：

- 控制台编辑的是工作副本（`config_keys`），提交后冻结成不可变的发布快照（`config_releases`），版本号递增；回滚 = 把旧快照作为新版本重新发布，并把工作副本恢复成它。
- 生产（`ZEUS_ENV=prod`）提交需 owner 审批；只改动已有 killswitch 配置项、或提交人是唯一 owner 时直接上线。其他环境提交即上线。
- api 进程在内存里持有线上快照，发布时经 Redis 频道 `zeus:config:published` 通知刷新，另每 30 秒比对版本兜底；数据库或 Redis 故障时继续用上一版。
- App 调 `GET /v1/config`（ETag / 304），每个 `/v1` 响应带 `X-Zeus-Config-Version`。规则只在服务端计算，`clientVisible=false` 的配置项不下发。
- 服务端也读同一份配置：`quota.free.*` 决定免费额度（用户级覆盖仍优先），`report.create_enabled` / `report.pdf_enabled` 关闭后接口返回 503。
- 内置配置项由 admin 进程启动时和 `zeusctl seed` 补齐，已存在的不覆盖。`ZEUS_REGION`（`china` 默认 / `international`）供 region 条件使用。

开发环境下（`ZEUS_ENV=dev`，默认）：

- 短信验证码固定为 `ZEUS_DEV_SMS_CODE`，不设置则打印在 api 日志里；
- Apple 登录的 identityToken 传 `dev:<任意 id>`，微信登录 code 传 `dev:<任意 id>`，一键登录 token 传 `dev:+86手机号`；
- 模型和搜索都是 mock，输出可预测，整条简报链路不需要任何外部账号。

`config.Load` 在非 dev 环境拒绝这些开发设置。配置项见 `server/internal/platform/config`，全部来自 `ZEUS_*` 环境变量。

### 接真实服务

都是可选的，不设置就用 mock 或返回“即将开放”：

| 能力 | 环境变量 |
| --- | --- |
| 模型网关 | `ZEUS_LLM_PROVIDER=gateway`、`ZEUS_MODELGW_CONFIG=deploy/modelgw.example.yaml`，以及该文件里 `api_key_env` 指定的 key（如 `ZEUS_QWEN_API_KEY`），或设 `ZEUS_SECRETS_KEY` 后在控制台“服务商”页填写；路由文件只在首次启动时导入，之后在控制台“模型路由”页修改 |
| 内容审核词表 | `ZEUS_MODERATION_KEYWORDS_FILE`（每行一个词；非 dev 环境必填） |
| 联网搜索 | `ZEUS_SEARCH_PROVIDER=console`（旧值 `bocha` 等同）：用控制台「服务商」页配置的搜索服务商，支持博查 / Tavily / Brave / Serper，可设主用和备用，主用出错自动换备用，约 1 分钟生效；内置博查仍可用 `ZEUS_BOCHA_API_KEY`。网页读取走 `ZEUS_EGRESS_PROXY`（可选） |
| 高德 MCP | `ZEUS_MCP_AMAP_URL`（含 key 的完整 Streamable HTTP 地址） |
| PDF 导出 | api 和 orchestrator 都设 `ZEUS_BLOB_DIR`（同一目录）；orchestrator 设 `ZEUS_CHROMIUM_PATH`，容器里以 root 运行时加 `ZEUS_CHROMIUM_NO_SANDBOX=true` |
| 反馈截图 | api 设 `ZEUS_BLOB_DIR` 才能上传；admin 也设同一个 `ZEUS_BLOB_DIR`，并与 api 使用相同的 `ZEUS_FILES_SECRET`（或相同的 `ZEUS_JWT_SECRET`）和 `ZEUS_PUBLIC_BASE_URL`，控制台里的截图链接才能打开 |
| 反馈回复推送 | admin 进程也配置上面的 APNs 变量；未配置时回复照常保存，只是不推送 |
| 消息推送 | orchestrator 和 admin 都配置 APNs 和 EMAS 变量。所有通知先进消息中心；免打扰期间的推送由 orchestrator 每分钟补发；运营推送活动由 owner 审批后在 orchestrator 里分批发送（每批 500 人）。运营推送频控默认每人每天 1 条、7 天 3 条（`notify.DefaultLimits`，接入配置中心后改为远程配置） |
| 短信验证码 | `ZEUS_ALIYUN_ACCESS_KEY_ID`、`ZEUS_ALIYUN_ACCESS_KEY_SECRET`、`ZEUS_SMS_SIGN_NAME`、`ZEUS_SMS_TEMPLATE_CODE`（模板变量 `${code}`） |
| 一键登录 | 同上 AccessKey，加 `ZEUS_ONECLICK_ENABLED=true` |
| Apple 登录 | `ZEUS_APPLE_CLIENT_IDS`（Bundle ID，多个用逗号分隔） |
| 微信登录 | `ZEUS_WECHAT_APP_ID`、`ZEUS_WECHAT_SECRET`（App 需绑定开放平台账号，才有 unionid） |
| APNs 推送 | `ZEUS_APNS_KEY_FILE`（.p8）、`ZEUS_APNS_KEY_ID`、`ZEUS_APNS_TEAM_ID`、`ZEUS_APNS_TOPIC` |
| 安卓推送（阿里云 EMAS） | 短信同一组 AccessKey，加 `ZEUS_EMAS_APP_KEY`（EMAS 控制台的 AppKey）、`ZEUS_EMAS_POPUP_ACTIVITY`（`com.jasonhu.zeus.app.push.PushPopupActivity`，厂商通道辅助弹窗）。厂商通道（华为/荣耀/小米/OPPO/vivo）的 AppID 和密钥配在 EMAS 控制台，不在服务端。和 APNs 可同时配置，按设备平台分别发送 |

非 dev 环境里，没配置的登录方式会返回 `unavailable`（“该登录方式暂未开放”），不会退回到假实现。

## 部署到 K8s

镜像（`deploy/docker/Dockerfile`）和 Helm chart（`deploy/helm/zeus`）的用法见 [deploy/helm/README.md](../deploy/helm/README.md)。

## 测试

```sh
export ZEUS_TEST_DATABASE_URL=postgres://zeus:zeus@localhost:5432/postgres?sslmode=disable
export ZEUS_TEST_REDIS_URL=redis://localhost:6379/0
export ZEUS_TEST_TEMPORAL_HOST=localhost:7233
export ZEUS_TEST_CHROMIUM=/path/to/chrome   # 可选：用真 Chromium 测 PDF，不设则用假打印
go test -race ./...
```

没设置这些变量的测试会跳过。`httpapi` 的端到端测试会把每个响应按 `api/openapi.yaml` 校验，保证 iOS 生成的客户端能解码。

## 改接口 / 改 SQL

```sh
go generate ./server/...   # 重新生成 httpapi/gen 和 db
```

CI 会检查生成代码与 `api/openapi.yaml`、`queries/*.sql` 一致。

## 中文 / English

- 用户有两个语言：`users.ui_language`（App 显示语言，zh / en）和 `users.report_language`（auto 跟随显示语言）。
  简报、推送、消息中心、进度步骤跟随显示语言；报告按 `reports.language` 生成，新建时可单次指定。
- 请求的 `Accept-Language` 决定错误提示和官方模板文案的语言（`internal/i18n`，在 `httpapi` 中间件里设置）。
- 错误提示：服务里照旧写中文，`i18n/messages.go` 按中文原文给出英文；`TestCatalogCoversErrors` 会扫出漏翻的提示。
- 推送：`push.Notification` 填 `TitleEn` / `BodyEn`，`notify.Service` 按用户显示语言挑选；运营推送的英文版选填。
- 生成：提示词仍是中文，`i18n.OutputInstruction` 在末尾指定输出语言；篇幅提示分中英两套。
- 官方模板：`prompts/briefing-templates/*.yaml` 的 `translations.en` 写英文文案（缺的回落中文），控制台也可编辑。
