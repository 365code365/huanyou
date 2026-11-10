#!/usr/bin/env bash
# Deploy the paycenter to the production server.
#
#   paycenter/scripts/deploy.sh          (or: make -C paycenter deploy)
#
# Builds the static Linux binary from the committed code, backs up the live
# binary and database, swaps the binary, restarts the service and checks
# health. If the new binary does not come up healthy, the old one is put back.
#
# Server address and password come from doc/服务器.txt (not in git):
#   line 1  到期日期：YYYY年MM月DD日 HH:MM ...   (no deploy after this date)
#   line 2  host
#   line 3  user
#   line 4  password
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PC="$ROOT/paycenter"
CRED="$ROOT/doc/服务器.txt"
PUBLIC_URL=${PAYCENTER_PUBLIC_URL:-https://shoping.rely-vpn-go.com}
KEEP=5 # backups kept on the server

die() { echo "deploy: $*" >&2; exit 1; }

[ -f "$CRED" ] || die "找不到 $CRED"
HOST=$(sed -n 2p "$CRED" | tr -d '[:space:]')
USER_=$(sed -n 3p "$CRED" | tr -d '[:space:]')
SSHPASS=$(sed -n 4p "$CRED" | tr -d '\r\n')
[ -n "$HOST" ] && [ -n "$USER_" ] && [ -n "$SSHPASS" ] || die "$CRED 格式不对（第 2-4 行：地址 / 用户 / 密码）"
export SSHPASS

# The server is rented until the date on line 1; do not deploy after it.
EXP=$(sed -n 1p "$CRED" | sed -nE 's/.*([0-9]{4})年([0-9]{2})月([0-9]{2})日 *([0-9]{2}:[0-9]{2}).*/\1-\2-\3 \4/p')
if [ -n "$EXP" ] && [[ "$(date '+%Y-%m-%d %H:%M')" > "$EXP" ]]; then
  die "服务器已于 $EXP 到期，按约定不再部署"
fi

# Deploy exactly what is committed.
if [ -n "$(git -C "$ROOT" status --porcelain -- paycenter)" ] && [ "${FORCE:-}" != 1 ]; then
  git -C "$ROOT" status --short -- paycenter >&2
  die "paycenter 有未提交的改动，先提交再部署（或 FORCE=1）"
fi
REV=$(git -C "$ROOT" rev-parse --short HEAD)

command -v sshpass >/dev/null || die "需要 sshpass（brew install hudochenkov/sshpass/sshpass）"
# Password only: offering every local key first trips the server's MaxAuthTries.
SSH_OPTS=(-o ConnectTimeout=15 -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=accept-new)
remote() { sshpass -e ssh "${SSH_OPTS[@]}" "$USER_@$HOST" "$@"; }

echo "==> 构建 $REV"
make -C "$PC" linux >/dev/null
BIN="$PC/bin/linux-amd64/paycenter"
SUM=$(shasum -a 256 "$BIN" | cut -c1-64)

TS=$(date +%Y%m%d%H%M%S)
echo "==> 备份线上程序和数据库（$TS）"
remote "set -e
  cp -a /usr/local/bin/paycenter /opt/paycenter/bin/paycenter.bak-$TS
  sqlite3 /var/lib/paycenter/paycenter.db '.backup /opt/paycenter/paycenter.db.bak-$TS'
  ls -1t /opt/paycenter/bin/paycenter.bak-* | tail -n +$((KEEP + 1)) | xargs -r rm -f
  ls -1t /opt/paycenter/paycenter.db.bak-* | tail -n +$((KEEP + 1)) | xargs -r rm -f"

echo "==> 上传"
sshpass -e scp "${SSH_OPTS[@]}" -q "$BIN" "$USER_@$HOST:/usr/local/bin/paycenter.new"

echo "==> 替换并重启"
remote "set -e
  cd /usr/local/bin
  [ \"\$(sha256sum paycenter.new | cut -c1-64)\" = $SUM ] || { echo '校验和不一致'; rm -f paycenter.new; exit 1; }
  chmod 755 paycenter.new && mv -f paycenter.new paycenter
  systemctl restart paycenter
  for i in \$(seq 1 15); do
    sleep 1
    if systemctl is-active -q paycenter && curl -fsS -m 3 http://127.0.0.1:8090/healthz >/dev/null 2>&1; then echo ok; exit 0; fi
  done
  echo '新版本启动失败，回滚到 paycenter.bak-$TS'
  journalctl -u paycenter --no-pager -n 20
  cp -a /opt/paycenter/bin/paycenter.bak-$TS /usr/local/bin/paycenter
  systemctl restart paycenter
  exit 1" || die "部署失败，已回滚（数据库备份 /opt/paycenter/paycenter.db.bak-$TS）"

code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$PUBLIC_URL/healthz" || true)
[ "$code" = 200 ] || die "服务已重启，但公网 $PUBLIC_URL/healthz 返回 $code"
echo "==> 完成：$REV 已部署到 $HOST（公网 healthz 200，备份 $TS）"
