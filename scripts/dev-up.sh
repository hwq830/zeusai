#!/usr/bin/env bash
# 本地一键起服务端：依赖（PG16+pgvector / Redis / Temporal）→ 迁移 → 种子数据 → 控制台账号
# → api(:8080) / orchestrator / admin(:8081)。日志在 .dev/logs，停止用 scripts/dev-down.sh。
#
# 依赖已经在本机端口上跑着（Homebrew 或别的方式）就直接用；否则用 Docker 起 deploy/local/compose.yaml。
# Homebrew 方式（国内不用翻 Docker Hub）：
#   brew install postgresql@16 pgvector redis temporal jq
#   brew services start postgresql@16 && brew services start redis
#   createuser -s zeus; psql -d postgres -c "alter role zeus password 'zeus'"; createdb -O zeus zeus
#   temporal server start-dev --headless &      # 或者交给本脚本自动起
#
# 可选环境变量：ZEUS_DEV_SMS_CODE（默认 246810）、ZEUS_ADMIN_EMAIL / ZEUS_ADMIN_PASSWORD（控制台账号，
# 默认 admin@zeus.local / zeus-admin-dev），以及 server/README.md 里接真实服务的 ZEUS_* 变量，会原样传给各进程。
set -euo pipefail
cd "$(dirname "$0")/.."

DEV=.dev
mkdir -p "$DEV/bin" "$DEV/logs" "$DEV/pids" "$DEV/blob"

export ZEUS_DEV_SMS_CODE=${ZEUS_DEV_SMS_CODE:-246810}
export ZEUS_BLOB_DIR=${ZEUS_BLOB_DIR:-$PWD/$DEV/blob}
ADMIN_EMAIL=${ZEUS_ADMIN_EMAIL:-admin@zeus.local}
ADMIN_PASSWORD=${ZEUS_ADMIN_PASSWORD:-zeus-admin-dev}

port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

need=()
port_open 5432 || need+=(postgres)
port_open 6379 || need+=(redis)
port_open 7233 || need+=(temporal)
if ((${#need[@]})); then
  if [[ " ${need[*]} " == " temporal " ]] && command -v temporal >/dev/null; then
    echo "▶ 启动 temporal dev server"
    nohup temporal server start-dev --headless --log-level error >"$DEV/logs/temporal.log" 2>&1 &
    echo $! >"$DEV/pids/temporal"
  elif command -v docker >/dev/null; then
    echo "▶ docker compose 启动 ${need[*]}"
    docker compose -f deploy/local/compose.yaml up -d --wait "${need[@]}"
  else
    echo "缺少 ${need[*]}：装 Docker，或按脚本开头的 Homebrew 方式安装" >&2; exit 1
  fi
  for i in $(seq 1 60); do port_open 7233 && break; sleep 1; done
fi
port_open 7233 || { echo "Temporal 没起来（看 $DEV/logs/temporal.log 或 docker compose logs temporal）" >&2; exit 1; }

echo "▶ 编译"
go build -o "$DEV/bin/" ./server/cmd/...

echo "▶ 迁移 + 种子数据"
"$DEV/bin/zeusctl" migrate
"$DEV/bin/zeusctl" seed
if out=$(ZEUS_ADMIN_PASSWORD=$ADMIN_PASSWORD "$DEV/bin/zeusctl" admin-create "$ADMIN_EMAIL" owner 2>&1); then
  echo "$out"
elif [[ $out == *duplicate* ]]; then
  echo "控制台账号 $ADMIN_EMAIL 已存在"
else
  echo "$out" >&2; exit 1
fi

# 真机连 Mac 时用局域网地址，分享链接也要能在手机上打开。
LAN_IP=$( (ipconfig getifaddr en0 || ipconfig getifaddr en1 || hostname -I | awk '{print $1}') 2>/dev/null || true)
export ZEUS_PUBLIC_BASE_URL=${ZEUS_PUBLIC_BASE_URL:-http://${LAN_IP:-localhost}:8080}

if [[ -z ${ZEUS_CHROMIUM_PATH:-} ]]; then
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium" \
           "$(command -v chromium || true)" "$(command -v google-chrome || true)"; do
    [[ -n $c && -x $c ]] && { export ZEUS_CHROMIUM_PATH=$c; break; }
  done
fi

scripts/dev-down.sh --quiet
for p in api orchestrator admin; do
  nohup "$DEV/bin/$p" >"$DEV/logs/$p.log" 2>&1 &
  echo $! >"$DEV/pids/$p"
done
for i in $(seq 1 30); do port_open 8080 && port_open 8081 && break; sleep 0.5; done
port_open 8080 || { tail -n 20 "$DEV/logs/api.log" >&2; exit 1; }

cat <<MSG

✅ 服务端已启动
  App 接口     http://localhost:8080   （真机：$ZEUS_PUBLIC_BASE_URL）
  控制台接口   http://localhost:8081   账号 $ADMIN_EMAIL / $ADMIN_PASSWORD
  短信验证码   $ZEUS_DEV_SMS_CODE（任意手机号）
  PDF 导出     ${ZEUS_CHROMIUM_PATH:-未找到 Chrome，PDF 不可用}
  日志         $DEV/logs/{api,orchestrator,admin}.log
  冒烟测试     ZEUS_SMOKE_ADMIN_EMAIL=$ADMIN_EMAIL ZEUS_SMOKE_ADMIN_PASSWORD=$ADMIN_PASSWORD scripts/smoke.sh
  停止         scripts/dev-down.sh
MSG
