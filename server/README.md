# Zeus 服务端

一个 Go module（仓库根目录 `go.mod`），编译出多个进程：

| 进程 | 入口 | 状态 |
| --- | --- | --- |
| `api` | `server/cmd/api` | 登录、额度、模板、定时简报、今日简报、任务与 SSE、连接器、记忆 |
| `orchestrator` | `server/cmd/orchestrator` | Temporal worker：每日简报工作流 |
| `zeusctl` | `server/cmd/zeusctl` | `migrate` 迁移数据库，`seed` 导入官方模板和连接器目录 |
| `modelgw` / `sandboxmgr` / `egress` / `sandboxd` | 待写 | 第 3–4 阶段 |

## 目录

```
server/
├── cmd/                     # 各进程 main
├── migrations/              # goose SQL 迁移（嵌入二进制）
└── internal/
    ├── httpapi/             # 实现 api/openapi.yaml 生成的 strict server；gen/ 为生成代码
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

开发环境下（`ZEUS_ENV=dev`，默认）：

- 短信验证码固定为 `ZEUS_DEV_SMS_CODE`，不设置则打印在 api 日志里；
- Apple 登录的 identityToken 传 `dev:<任意 id>`，微信登录 code 传 `dev:<任意 id>`，一键登录 token 传 `dev:+86手机号`；
- 模型和搜索都是 mock，输出可预测，整条简报链路不需要任何外部账号。

`config.Load` 在非 dev 环境拒绝这些开发设置。配置项见 `server/internal/platform/config`，全部来自 `ZEUS_*` 环境变量。

## 测试

```sh
export ZEUS_TEST_DATABASE_URL=postgres://zeus:zeus@localhost:5432/postgres?sslmode=disable
export ZEUS_TEST_REDIS_URL=redis://localhost:6379/0
export ZEUS_TEST_TEMPORAL_HOST=localhost:7233
go test -race ./...
```

没设置这些变量的测试会跳过。`httpapi` 的端到端测试会把每个响应按 `api/openapi.yaml` 校验，保证 iOS 生成的客户端能解码。

## 改接口 / 改 SQL

```sh
go generate ./server/...   # 重新生成 httpapi/gen 和 db
```

CI 会检查生成代码与 `api/openapi.yaml`、`queries/*.sql` 一致。
