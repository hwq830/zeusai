package remoteconfig

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/db"
)

// Keys read by server code. Their defaults here and in the App are what applies until a
// release says otherwise, so deleting a key from the console never breaks either side.
const (
	KeyReportCreateEnabled  = "report.create_enabled"
	KeyReportPDFEnabled     = "report.pdf_enabled"
	KeyQuotaSubscriptions   = "quota.free.subscriptions"
	KeyQuotaTokensPerMonth  = "quota.free.tokens_per_month"
	KeyQuotaTokenWeights    = "quota.token_weights"
	KeyQuotaReportMinTokens = "quota.report_min_tokens"
	KeyQuotaTaskTokenCap    = "quota.task_token_cap"
	KeyQuotaReportEstimates = "quota.report_estimates"
	KeyAppMinVersion        = "app.min_version"
	KeyAppRecommended       = "app.recommended_version"
	KeyHomeNotice           = "home.notice"
	KeyTemplateSharing      = "template.sharing_enabled"
	KeyLiveBrowserView      = "task.live_browser_view"
)

// Builtin describes a key created on startup when missing. Existing keys are never touched,
// so console edits survive restarts.
type Builtin struct {
	Key, Namespace, Type, Description, ApplyMode string
	Default, Schema                              string
	ClientVisible, Expose                        bool
}

var noticeSchema = `{"type":["object","null"],"required":["text"],"additionalProperties":false,
"properties":{"text":{"type":"string","minLength":1,"maxLength":200},"level":{"enum":["info","warning","critical"]},
"link":{"type":"string","pattern":"^(https://|zeus://)"}}}`

var tokenWeightsSchema = `{"type":"object","minProperties":1,"additionalProperties":{"type":"object",
"required":["input","output","cached"],"additionalProperties":false,"properties":{
"input":{"type":"number","minimum":0},"output":{"type":"number","minimum":0},"cached":{"type":"number","minimum":0}}}}`

// Weights relative to qwen-max (weight 1), from the example price list in
// deploy/modelgw.example.yaml; re-derive when the real prices are known.
const defaultTokenWeights = `{"default":{"input":1,"output":1,"cached":1},
"qwen-max":{"input":1,"output":1,"cached":0.4},
"qwen-turbo":{"input":0.1,"output":0.1,"cached":0.04},
"deepseek-chat":{"input":0.85,"output":0.85,"cached":0.2}}`

const defaultReportEstimates = `{"quick":{"min":200000,"max":400000},
"standard":{"min":400000,"max":800000},"deep":{"min":800000,"max":1500000}}`

var reportEstimatesSchema = `{"type":"object","additionalProperties":false,"properties":{
"quick":{"$ref":"#/$defs/r"},"standard":{"$ref":"#/$defs/r"},"deep":{"$ref":"#/$defs/r"}},
"$defs":{"r":{"type":"object","required":["min","max"],"additionalProperties":false,
"properties":{"min":{"type":"integer","minimum":0},"max":{"type":"integer","minimum":0}}}}}`

var Builtins = []Builtin{
	{Key: KeyReportCreateEnabled, Namespace: NSKillswitch, Type: TypeBool, Default: "true", ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "关闭后 App 隐藏「研究报告」入口，服务端拒绝新建报告"},
	{Key: KeyReportPDFEnabled, Namespace: NSKillswitch, Type: TypeBool, Default: "true", ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "关闭后 App 隐藏 PDF 导出，服务端拒绝导出"},
	{Key: KeyQuotaSubscriptions, Namespace: NSQuota, Type: TypeInt, Default: "2", ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "免费用户最多同时保留的定时简报数；用户级覆盖仍在用户详情页设置"},
	{Key: KeyQuotaTokensPerMonth, Namespace: NSQuota, Type: TypeInt, Default: "7500000", ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "免费用户每月 token 额度（计费 token，按 quota.token_weights 折算）；用户级覆盖和加赠在用户详情页设置"},
	{Key: KeyQuotaTokenWeights, Namespace: NSQuota, Type: TypeJSON, Default: defaultTokenWeights,
		Schema: tokenWeightsSchema, ApplyMode: ApplyImmediate, ClientVisible: false,
		Description: "各模型的计费 token 折算系数：{模型名: {input, output, cached}}，以 qwen-max 为 1，未列出的模型用 default；只影响之后的调用，不对用户展示"},
	{Key: KeyQuotaReportMinTokens, Namespace: NSQuota, Type: TypeInt, Default: "500000", ApplyMode: ApplyImmediate, ClientVisible: false,
		Description: "剩余 token 低于此值时不能发起新的调研报告（定时简报不受影响）"},
	{Key: KeyQuotaTaskTokenCap, Namespace: NSQuota, Type: TypeInt, Default: "1500000", ApplyMode: ApplyImmediate, ClientVisible: false,
		Description: "单个任务最多消耗的计费 token；超过后任务失败，且不计入用户额度；0 表示不限"},
	{Key: KeyQuotaReportEstimates, Namespace: NSQuota, Type: TypeJSON, Default: defaultReportEstimates,
		Schema: reportEstimatesSchema, ApplyMode: ApplyImmediate, ClientVisible: false,
		Description: "发起报告前展示的预计消耗（计费 token）：{quick|standard|deep: {min, max}}"},
	{Key: KeyAppMinVersion, Namespace: NSApp, Type: TypeString, Default: `""`, ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "低于此版本的 App 显示不可关闭的升级页；空字符串表示不限制"},
	{Key: KeyAppRecommended, Namespace: NSApp, Type: TypeString, Default: `""`, ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "低于此版本的 App 提示一次可关闭的升级建议"},
	{Key: KeyHomeNotice, Namespace: NSUI, Type: TypeJSON, Default: "null", Schema: noticeSchema, ApplyMode: ApplyImmediate, ClientVisible: true,
		Description: "首页公告条：{text, level: info|warning|critical, link}；null 不显示"},
	{Key: KeyTemplateSharing, Namespace: NSFeature, Type: TypeBool, Default: "false", ApplyMode: ApplyNextLaunch, ClientVisible: true, Expose: true,
		Description: "模板分享到市场（V2）"},
	{Key: KeyLiveBrowserView, Namespace: NSFeature, Type: TypeBool, Default: "false", ApplyMode: ApplyNextLaunch, ClientVisible: true, Expose: true,
		Description: "实时围观助理浏览器（V2）"},
}

// SeedBuiltins creates missing built-in keys and reports how many it created.
func SeedBuiltins(ctx context.Context, q *db.Queries) (int, error) {
	n := 0
	for _, b := range Builtins {
		var schema json.RawMessage
		if b.Schema != "" {
			schema = json.RawMessage(b.Schema)
		}
		_, err := q.InsertConfigKey(ctx, db.InsertConfigKeyParams{
			Key: b.Key, Namespace: b.Namespace, Type: b.Type, Description: b.Description, Owner: "",
			DefaultValue: json.RawMessage(b.Default), ValueSchema: schema, ApplyMode: b.ApplyMode,
			ClientVisible: b.ClientVisible, Expose: b.Expose,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // already there
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
