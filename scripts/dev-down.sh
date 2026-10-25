#!/usr/bin/env bash
# 停掉 scripts/dev-up.sh 起的进程。加 --deps 连 docker compose 依赖一起停（数据卷保留）。
set -euo pipefail
cd "$(dirname "$0")/.."
quiet=false deps=false
for a in "$@"; do case $a in --quiet) quiet=true ;; --deps) deps=true ;; esac; done
procs=(api orchestrator admin)
$deps && procs+=(temporal)
for p in "${procs[@]}"; do
  f=.dev/pids/$p
  [[ -f $f ]] || continue
  pid=$(cat "$f")
  kill "$pid" 2>/dev/null && ! $quiet && echo "已停止 $p ($pid)"
  rm -f "$f"
done
if $deps && command -v docker >/dev/null; then
  docker compose -f deploy/local/compose.yaml stop
fi
