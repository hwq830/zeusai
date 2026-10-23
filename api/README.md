# 接口契约

`openapi.yaml` 是 App 与服务端之间的唯一契约（OpenAPI 3.0.3）。改接口先改这里。

- 服务端：`go generate ./server/internal/httpapi/gen` 用 oapi-codegen 生成 chi strict server 和模型。
- iOS：在 zeusai-ios 里用 swift-openapi-generator 从本文件生成 Client。
- 兼容规则：只加字段不删字段；客户端忽略未知字段、未知枚举值和未知事件类型。
- 任务事件流：`GET /v1/tasks/{taskId}/events`（SSE），每条 `data` 是一个 `TaskEvent`，`id` 为递增序号，断线带 `Last-Event-ID` 续传。

用 3.0.3 而不是 3.1，是因为 oapi-codegen 对 3.1 的支持还不完整；swift-openapi-generator 两者都支持。
