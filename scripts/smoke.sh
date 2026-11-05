#!/usr/bin/env bash
# 联调冒烟：对本地（或任意）api / admin 进程走一遍 App 和控制台的主流程。
#   scripts/smoke.sh                       # 默认 http://localhost:8080 和 :8081
#   ZEUS_API=http://192.168.1.10:8080 scripts/smoke.sh
# 需要 api 以 ZEUS_DEV_SMS_CODE 启动（默认 246810），orchestrator 在跑；控制台检查需要
# ZEUS_SMOKE_ADMIN_EMAIL / ZEUS_SMOKE_ADMIN_PASSWORD（zeusctl admin-create 创建的账号），不设则跳过。
#
# 变量后面紧跟中文或全角标点时，一律写成 ${VAR}：macOS 自带的 bash 3.2 在 UTF-8
# locale 下会把紧跟变量的多字节字符并进变量名，然后报 unbound variable。
set -euo pipefail

API=${ZEUS_API:-http://localhost:8080}
ADMIN=${ZEUS_ADMIN_API:-http://localhost:8081}
CODE=${ZEUS_DEV_SMS_CODE:-246810}
PHONE=${ZEUS_SMOKE_PHONE:-+86138$(printf '%08d' $((RANDOM * RANDOM % 100000000)))}
TIMEOUT=${ZEUS_SMOKE_TIMEOUT:-120}

command -v jq >/dev/null || { echo "需要 jq（brew install jq）" >&2; exit 1; }

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; exit 1; }

# call METHOD PATH [JSON] → 打印响应体；非 2xx 直接失败。
call() {
  local method=$1 path=$2 body=${3:-} out status
  out=$(mktemp)
  status=$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "$BASE$path" \
    -H 'Content-Type: application/json' ${TOKEN:+-H "Authorization: Bearer $TOKEN"} \
    ${body:+--data "$body"}) || fail "$method $path 连不上 $BASE"
  [[ $status == 2* ]] || { cat "$out" >&2; echo >&2; rm -f "$out"; fail "$method $path → HTTP $status"; }
  cat "$out"; rm -f "$out"
}

# wait_task ID → 轮询任务直到结束，打印最终状态。
wait_task() {
  local id=$1 status deadline=$((SECONDS + TIMEOUT))
  while (( SECONDS < deadline )); do
    status=$(call GET "/v1/tasks/$id" | jq -r .status)
    case $status in done|failed|cancelled) echo "$status"; return ;; esac
    sleep 2
  done
  echo timeout
}

echo "App 接口 $API"
BASE=$API TOKEN=
curl -sf "$API/healthz" >/dev/null || fail "healthz 不通：api 没启动，或手机/模拟器连不到 $API"
pass healthz

call GET /v1/config >/dev/null && pass "远程配置 /v1/config"

call POST /v1/auth/sms/send "{\"phone\":\"$PHONE\"}" >/dev/null
SESSION=$(call POST /v1/auth/sms/verify "{\"phone\":\"$PHONE\",\"code\":\"$CODE\"}")
TOKEN=$(jq -r .accessToken <<<"$SESSION")
REFRESH=$(jq -r .refreshToken <<<"$SESSION")
[[ -n $TOKEN && $TOKEN != null ]] || fail "短信登录没有返回 accessToken"
pass "短信登录 ${PHONE}（新用户：$(jq -r .isNewUser <<<"$SESSION")）"

call GET /v1/me >/dev/null && pass "/v1/me"
call GET /v1/me/usage | jq -c . >/dev/null && pass "/v1/me/usage 额度"
call POST /v1/devices '{"pushToken":"smoke-token-0000","platform":"ios","apnsEnv":"sandbox","appVersion":"0.1.0","locale":"zh-Hans"}' >/dev/null
pass "注册推送设备"

TEMPLATES=$(call GET /v1/templates)
TEMPLATE_ID=$(jq -r '.items[0].id' <<<"$TEMPLATES")
[[ $TEMPLATE_ID != null ]] || fail "没有官方模板：先跑 zeusctl seed"
pass "模板列表 $(jq '.items | length' <<<"$TEMPLATES") 个"

SUB=$(call POST /v1/briefing-subscriptions "{\"templateId\":\"$TEMPLATE_ID\",\"name\":\"联调简报\",\"topics\":[\"人工智能\"],\"schedule\":{\"freq\":\"daily\",\"time\":\"08:00\",\"tz\":\"Asia/Shanghai\"}}")
SUB_ID=$(jq -r .id <<<"$SUB")
pass "创建定时简报 $SUB_ID"

RUN=$(call POST "/v1/briefing-subscriptions/$SUB_ID/run")
RUN_TASK=$(jq -r .id <<<"$RUN")
status=$(wait_task "$RUN_TASK")
[[ $status == done ]] || fail "立即生成简报：任务 ${RUN_TASK} 状态 ${status}（看 orchestrator 日志）"
pass "立即生成简报（Temporal → orchestrator → 消息中心）"
TODAY=$(call GET /v1/briefings/today) && pass "今日简报"
BRIEFING_ID=$(jq -r '[.items[] | select(.status == "ready")][0].id // empty' <<<"$TODAY")
if [[ -n $BRIEFING_ID ]]; then
  BSHARE=$(call POST "/v1/briefings/$BRIEFING_ID/shares" '{"scope":"full","channel":"link"}')
  pass "简报分享链接 $(jq -r .url <<<"$BSHARE")"
fi

TASK=$(call POST /v1/tasks '{"kind":"report","topic":"2026 年国内大模型应用市场格局","depth":"quick"}')
TASK_ID=$(jq -r .id <<<"$TASK")
# SSE：读到第一条事件就说明流式通道通了（模拟器/真机走的是同一个接口）。
first=$(curl -sN --max-time 20 -H "Authorization: Bearer $TOKEN" -H 'Accept: text/event-stream' "$API/v1/tasks/$TASK_ID/events" | grep -m1 '^event:\|^data:' || true)
[[ -n $first ]] || fail "SSE /v1/tasks/$TASK_ID/events 没有收到事件"
pass "SSE 事件流"
status=$(wait_task "$TASK_ID")
[[ $status == done ]] || fail "调研报告：任务 $TASK_ID 状态 $status"
REPORT_ID=$(call GET "/v1/tasks/$TASK_ID" | jq -r .reportId)
call GET "/v1/reports/$REPORT_ID" >/dev/null && pass "调研报告 $REPORT_ID"
SHARE=$(call POST "/v1/reports/$REPORT_ID/shares" '{"scope":"full"}')
pass "分享链接 $(jq -r .url <<<"$SHARE")"

call GET /v1/messages >/dev/null && pass "消息中心"
call GET /v1/connectors >/dev/null && pass "连接器列表"
call POST /v1/feedback '{"type":"bug","content":"联调冒烟测试反馈"}' >/dev/null && pass "提交反馈"

TOKEN=$(call POST /v1/auth/refresh "{\"refreshToken\":\"$REFRESH\"}" | jq -r .accessToken)
pass "刷新令牌"

if [[ -n ${ZEUS_SMOKE_ADMIN_EMAIL:-} ]]; then
  echo "控制台接口 $ADMIN"
  BASE=$ADMIN TOKEN=
  TOKEN=$(call POST /admin/v1/auth/login "{\"email\":\"$ZEUS_SMOKE_ADMIN_EMAIL\",\"password\":\"$ZEUS_SMOKE_ADMIN_PASSWORD\"}" | jq -r .token)
  pass "控制台登录"
  call GET "/admin/v1/users?q=$(jq -rn --arg p "$PHONE" '$p|@uri')" | jq -e '.items | length > 0' >/dev/null && pass "控制台能查到刚登录的用户"
  call GET /admin/v1/feedback >/dev/null && pass "控制台反馈列表"
  call GET /admin/v1/config/keys >/dev/null && pass "控制台配置中心"
else
  echo "（未设置 ZEUS_SMOKE_ADMIN_EMAIL，跳过控制台检查）"
fi

echo "全部通过"
