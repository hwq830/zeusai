# Zeus

Zeus（暂定名）是一款全云端运行的个人 AI 助理，面向 C 端用户，先做中国市场，再拓展海外。每个任务在独立的云端沙箱中执行，用户只需一个 iOS App 即可使用。

本仓库存放服务端、沙箱、接口契约等代码。iOS 客户端代码在 [hwq830/zeusai-ios](https://github.com/hwq830/zeusai-ios)。

## MVP 场景

- **每日简报**：官方模板 + 用户自定义模板，按计划定时生成并推送；可导出 PDF、分享整份或单条（H5 链接，带有效期，可撤销）
- **研究报告**：多步检索、浏览、整理，输出可分享的报告（App / H5 / PDF 共用一套 HTML 模板）
- **开源连接器（MCP）**：仅接入白名单内的 MCP 服务

## 技术选型

| 领域 | 选型 |
| --- | --- |
| 客户端 | iOS 17+，SwiftUI + Observation 轻量 MVVM，SPM 按功能拆包；报告阅读用 WebView |
| 服务端 | Go，6 个进程：`api`（模块化单体）、`orchestrator`（Temporal worker）、`modelgw`、`sandboxmgr`、`egress`、`sandboxd` |
| Agent | 自研 agent loop + Temporal 编排 |
| 沙箱 | 每任务一个 Kata microVM，沙箱内仅提供无头浏览器 |
| 模型 | 模型网关统一接入国内大模型 |
| 基础设施 | 阿里云 + 托管 Kubernetes |
| 接口契约 | REST + OpenAPI，流式输出用 SSE |

## 仓库结构

```
zeusai/
├── server/                 # Go 服务端：api / orchestrator / modelgw / sandboxmgr / egress
├── sandbox/                # 沙箱镜像与沙箱内守护进程 sandboxd
├── web/
│   ├── report-template/    # 报告 HTML 模板（App WebView / H5 分享页 / PDF 共用）
│   ├── briefing-template/  # 简报 HTML 模板（H5 分享页 / PDF 共用）
│   └── admin/              # 运营后台
├── api/                    # OpenAPI 契约，服务端与 iOS 客户端代码由此生成
├── prompts/                # 提示词与简报模板
├── evals/                  # 评测集与评测脚本
└── deploy/                 # K8s 清单、Helm、基础设施配置
```

## 状态

服务端开发中，见 [server/README.md](server/README.md)：接口契约、数据库、登录、定时简报（mock 模型）已可端到端运行；调研报告、模型网关、沙箱为后续阶段。
