# Zeus 服务端

一个 Go module（仓库根目录 `go.mod`），编译出多个进程：

| 进程 | 入口 | 状态 |
| --- | --- | --- |
| `api` | `server/cmd/api` | 登录、额度、模板、定时简报、今日简报、任务与 SSE、连接器、记忆 |
| `orchestrator` | `server/cmd/orchestrator` | Temporal worker：每日简报工作流 |
| `admin` | `server/cmd/admin` | 管理员控制台接口 `/admin/v1`（契约 `api/admin.yaml`，前端在 hwq830/zeusai-admin），只在内网暴露 |
| `zeusctl` | `server/cmd/zeusctl` | `migrate` 迁移数据库，`seed` 导入官方模板和连接器目录，`admin-create` 创建控制台账号 |
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
    └── platform/            # 配置、数据库连接、测试工具
```

提示词和官方简报模板在仓库根目录 `prompts/`，是场景配置的唯一来源；每个任务记录所用的 `prompts.Version`。

## 本地运行

```sh
docker compose -f deploy/local/compose.yaml up -d      # 或自己装 PG16+pgvector、Redis、temporal CLI
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
| `ZEUS_MODELGW_CONFIG`、`ZEUS_LLM_PROVIDER` | 与 api 相同，控制台“模型网关”页只读展示 |

角色：`owner` 全部权限；`operator` 用户、额度、模板、连接器、任务；`moderator` 内容审核和停用用户；`viewer` 只读。所有写操作进 `admin_audit_log`。在控制台改过的官方模板和连接器标记为 `admin_managed`，之后 `zeusctl seed` 不再覆盖它们的运营字段。控制台添加的屏蔽词与 `ZEUS_MODERATION_KEYWORDS_FILE` 合并，各进程约 1 分钟内生效，拦截记录写入 `moderation_events`。

开发环境下（`ZEUS_ENV=dev`，默认）：

- 短信验证码固定为 `ZEUS_DEV_SMS_CODE`，不设置则打印在 api 日志里；
- Apple 登录的 identityToken 传 `dev:<任意 id>`，微信登录 code 传 `dev:<任意 id>`，一键登录 token 传 `dev:+86手机号`；
- 模型和搜索都是 mock，输出可预测，整条简报链路不需要任何外部账号。

`config.Load` 在非 dev 环境拒绝这些开发设置。配置项见 `server/internal/platform/config`，全部来自 `ZEUS_*` 环境变量。

### 接真实服务

都是可选的，不设置就用 mock 或返回“即将开放”：

| 能力 | 环境变量 |
| --- | --- |
| 模型网关 | `ZEUS_LLM_PROVIDER=gateway`、`ZEUS_MODELGW_CONFIG=deploy/modelgw.example.yaml`，以及该文件里 `api_key_env` 指定的 key（如 `ZEUS_QWEN_API_KEY`） |
| 内容审核词表 | `ZEUS_MODERATION_KEYWORDS_FILE`（每行一个词；非 dev 环境必填） |
| 博查搜索 | `ZEUS_SEARCH_PROVIDER=bocha`、`ZEUS_BOCHA_API_KEY`；网页读取走 `ZEUS_EGRESS_PROXY`（可选） |
| 高德 MCP | `ZEUS_MCP_AMAP_URL`（含 key 的完整 Streamable HTTP 地址） |
| PDF 导出 | api 和 orchestrator 都设 `ZEUS_BLOB_DIR`（同一目录）；orchestrator 设 `ZEUS_CHROMIUM_PATH`，容器里以 root 运行时加 `ZEUS_CHROMIUM_NO_SANDBOX=true` |
| 短信验证码 | `ZEUS_ALIYUN_ACCESS_KEY_ID`、`ZEUS_ALIYUN_ACCESS_KEY_SECRET`、`ZEUS_SMS_SIGN_NAME`、`ZEUS_SMS_TEMPLATE_CODE`（模板变量 `${code}`） |
| 一键登录 | 同上 AccessKey，加 `ZEUS_ONECLICK_ENABLED=true` |
| Apple 登录 | `ZEUS_APPLE_CLIENT_IDS`（Bundle ID，多个用逗号分隔） |
| 微信登录 | `ZEUS_WECHAT_APP_ID`、`ZEUS_WECHAT_SECRET`（App 需绑定开放平台账号，才有 unionid） |
| APNs 推送 | `ZEUS_APNS_KEY_FILE`（.p8）、`ZEUS_APNS_KEY_ID`、`ZEUS_APNS_TEAM_ID`、`ZEUS_APNS_TOPIC` |

非 dev 环境里，没配置的登录方式会返回 `unavailable`（“该登录方式暂未开放”），不会退回到假实现。

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
