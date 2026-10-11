#!/usr/bin/env bash
# ARTEX 安装脚本：① 全部 Docker  ② 本地编译运行
set -euo pipefail
umask 077
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }

# ── docker 环境检测 / 自动安装 ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "已检测到 docker 与 docker compose"; return
  fi
  warn "未检测到 docker / docker compose"
  die "请先从 https://docs.docker.com/get-docker/ 安装 Docker 与 Compose，然后重试"

}

# ── ① 全部 Docker ───────────────────────────────
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres 密码（回车随机生成）' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY（可留空，后续在 UI 配）' '')"
    command -v python3 >/dev/null || die "请先安装 Python 3"
    ARTEX_INSTALL_PASSWORD="$pw" ARTEX_INSTALL_API_KEY="$key" python3 - <<'PYCONFIG'
import os
from pathlib import Path
# Compose DSNs use URL userinfo. Reject ambiguous metacharacters in this helper;
# the generated default uses only hex digits. Existing .env files are unchanged.
pw = os.environ['ARTEX_INSTALL_PASSWORD']
if not pw or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in pw):
    raise SystemExit('Use a non-empty URL-safe database password (letters, digits, _ or -).')
key = os.environ['ARTEX_INSTALL_API_KEY']
if any(c in key for c in "\r\n'"):
    raise SystemExit('API key contains unsupported quoting characters.')
values = {'POSTGRES_PASSWORD': pw, 'ANTHROPIC_API_KEY': key}
p = Path('.env')
lines = p.read_text().splitlines()
p.write_text('\n'.join(k + "='" + values[k] + "'" if (k := line.partition('=')[0]) in values else line for line in lines) + '\n')
p.chmod(0o600)
PYCONFIG
    ok "已生成 .env（POSTGRES_PASSWORD 已设置）"
  else
    info "沿用已存在的 .env"
  fi
  ./scripts/build-local-image.sh
  docker compose up -d --build
  ok "启动完成 → http://localhost:8787"
  info "查看日志：docker compose logs -f artex"
}

# ── ② 本地编译运行 ──────────────────────────────
install_local(){
  echo "数据库安装方式："
  echo "  1) 连接已有 PostgreSQL"
  echo "  2) 用 Docker 起一个 PostgreSQL（需要 docker）"
  case "$(ask '选择' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres 密码（回车随机）' "$(rand)")"
      docker run -d --name artex-pg -p 127.0.0.1:5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask '数据库地址' 127.0.0.1)"
      DB_PORT="$(ask '端口' 5432)"
      DB_USER="$(ask '账号' artex)"
      DB_PASS="$(ask '密码' '')"
      DB_NAME="$(ask '数据库名' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # 生成 config.json
  command -v python3 >/dev/null || die "请先安装 Python 3"
  export DB_HOST DB_PORT DB_USER DB_PASS DB_NAME DB_SSL
  python3 - <<'PYCONFIG'
import json, os
from pathlib import Path
config = {'database': {k: os.environ[e] for k,e in {
    'host':'DB_HOST', 'user':'DB_USER', 'password':'DB_PASS',
    'dbname':'DB_NAME', 'sslmode':'DB_SSL'}.items()}}
config['database']['port'] = int(os.environ['DB_PORT'])
p = Path('config.json')
p.write_text(json.dumps(config, indent=2) + '\n')
p.chmod(0o600)
PYCONFIG
  unset DB_PASS
  ok "已生成 config.json"

  # go 环境检查
  command -v go >/dev/null 2>&1 || die "未检测到 Go，请先安装 Go（>=1.26）：https://go.dev/dl/"
  ok "Go: $(go version)"

  # 内嵌前端需要 node 出静态产物
  if command -v npm >/dev/null 2>&1; then
    info "构建前端静态产物…"
    ( cd web && npm ci --ignore-scripts && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "编译内嵌单二进制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未检测到 npm：将编译**不内嵌前端**的后端（前端需另跑 npm run dev）"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "编译完成 → ./artex"

  info "启动…（Ctrl-C 退出）"
  ./artex
}

echo "=============================="
echo "  ARTEX 安装"
echo "  1) 全部 Docker 安装"
echo "  2) 本地运行（go 编译）"
echo "=============================="
case "$(ask '选择' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "无效选择" ;;
esac
