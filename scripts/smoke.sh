#!/usr/bin/env bash
# Drives the built kydrive-sync binary through ctl against a real kydrive-server, inside a
# private D-Bus session with its own gnome-keyring, so the desktop keyring and browser are
# never touched. Usage: smoke.sh <kydrive-server> <kydrive-sync>
set -euo pipefail
SRV_BIN=$1 SYNC_BIN=$2
W=$(mktemp -d)
PORT=18471
BASE=http://127.0.0.1:$PORT
FAIL=0
ok() { echo "  [ok]   $1"; }
bad() { echo "  [FAIL] $1"; FAIL=1; }
cleanup() { kill "${SYNC_PID:-}" "${SRV_PID:-}" 2>/dev/null || :; wait 2>/dev/null || :; rm -rf "$W"; }
trap cleanup EXIT

KY_ENV=development KY_CAPTCHA_PROVIDER=none KY_DB_DRIVER=sqlite KY_DATA_DIR=$W/data KY_BACKUP_DIR=$W/backups \
  KY_HOST=127.0.0.1 KY_PORT=$PORT KY_APP_URL=$BASE KY_ADMIN_PASSWORD='SmokeBootPass123!' \
  KY_SCIM_ENABLED=true KY_SCIM_TOKEN=smoke-scim-token-0123456789 "$SRV_BIN" >"$W/server.log" 2>&1 &
SRV_PID=$!
curl -s -o /dev/null --retry 30 --retry-delay 1 --retry-all-errors "$BASE/healthz"

J=(-s -b "$W/cookies" -c "$W/cookies" -H 'Content-Type: application/json')
csrf() { awk '$6 == "ky_csrf" { print $7 }' "$W/cookies"; }
curl "${J[@]}" -d '{"username":"admin","password":"SmokeBootPass123!"}' "$BASE/api/auth/login" >/dev/null
curl "${J[@]}" -H "X-CSRF-Token: $(csrf)" -d '{"current_password":"SmokeBootPass123!","new_password":"SmokeFinalPass456!"}' "$BASE/api/auth/change-password" >/dev/null
curl "${J[@]}" -d '{"username":"admin","password":"SmokeFinalPass456!"}' "$BASE/api/auth/login" >/dev/null
ME=$(curl "${J[@]}" "$BASE/api/auth/me" | jq -r .user.id)
GROUP=$(curl -s -H 'Authorization: Bearer smoke-scim-token-0123456789' -H 'Content-Type: application/scim+json' \
  -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"sync","members":[{"value":"'"$ME"'"}]}' "$BASE/scim/v2/Groups" | jq -r .id)
WS=$(curl "${J[@]}" -H "X-CSRF-Token: $(csrf)" -d '{"name":"Docs","quota":1073741824}' "$BASE/api/drive/workspaces" | jq -r .id)
curl "${J[@]}" -X PUT -H "X-CSRF-Token: $(csrf)" -d '{"role":"editor"}' "$BASE/api/drive/workspaces/$WS/grants/$GROUP" >/dev/null
printf 'from the server' | curl -s -b "$W/cookies" -H "X-CSRF-Token: $(csrf)" -H 'Content-Type: application/octet-stream' --data-binary @- \
  "$BASE/api/drive/workspaces/$WS/uploads?parent=&name=hello.txt&revision=0&mtime=2026-01-02T03:04:05Z" >/dev/null

# The "browser": approve with the admin session, then follow the loopback redirect.
mkdir -p "$W/bin"
cat >"$W/bin/xdg-open" <<EOF
#!/usr/bin/env bash
q=\$(python3 -c 'import sys,urllib.parse,json; print(json.dumps({k:v[0] for k,v in urllib.parse.parse_qs(urllib.parse.urlsplit(sys.argv[1]).query).items()}))' "\$1")
r=\$(curl -s -b "$W/cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: \$(awk '\$6 == "ky_csrf" { print \$7 }' "$W/cookies")" -d "\$q" "$BASE/api/devices/approve" | jq -r .redirect)
curl -s "\$r" >/dev/null &
EOF
chmod +x "$W/bin/xdg-open"

export HOME=$W/home XDG_CONFIG_HOME=$W/home/.config XDG_DATA_HOME=$W/home/.local/share XDG_RUNTIME_DIR=$W/run PATH=$W/bin:$PATH
mkdir -p "$HOME" "$XDG_RUNTIME_DIR" && chmod 700 "$XDG_RUNTIME_DIR"
dbus-run-session -- bash -s <<EOF
set -uo pipefail
FAIL=0
ok() { echo "  [ok]   \$1"; }
bad() { echo "  [FAIL] \$1"; FAIL=1; }
printf 'smoke' | gnome-keyring-daemon --unlock --components=secrets >/dev/null
"$SYNC_BIN" >"$W/sync.log" 2>&1 &
SYNC_PID=\$!
for i in \$(seq 50); do [ -S "$W/run/kydrive/sync.sock" ] && break; sleep 0.1; done
ctl() { "$SYNC_BIN" ctl "\$@"; }
waitfor() { for i in \$(seq 100); do eval "\$1" && return 0; sleep 0.1; done; return 1; }

[ "\$(stat -c %a "$W/run/kydrive/sync.sock")" = 600 ] && ok "socket is 0600" || bad "socket mode \$(stat -c %a "$W/run/kydrive/sync.sock")"
[ "\$(stat -c %a "$W/run/kydrive")" = 700 ] && ok "socket dir is 0700" || bad "socket dir mode"
ctl Link '{"Server":"$BASE","Name":"smoke laptop"}' >/dev/null && ok "Link via the approve page" || bad "Link"
TOKEN=\$(secret-tool lookup service KyDrive username "$BASE" 2>/dev/null || :)
case "\$TOKEN" in kyd_*) ok "token is in the Secret Service keyring";; *) bad "keyring lookup: '\$TOKEN'";; esac
ctl Workspaces | grep -q '"Name": "Docs"' && ok "Workspaces lists Docs" || bad "Workspaces"
ctl Choose '{"IDs":["$WS"]}' >/dev/null && ok "Choose" || bad "Choose"
waitfor '[ -f "$HOME/KyDrive/Docs/hello.txt" ]' && ok "hello.txt downloaded" || bad "download"
[ "\$(cat "$HOME/KyDrive/Docs/hello.txt" 2>/dev/null)" = "from the server" ] && ok "content matches" || bad "content"
[ "\$(stat -c %Y "$HOME/KyDrive/Docs/hello.txt")" = "\$(date -d 2026-01-02T03:04:05Z +%s)" ] && ok "server mtime applied" || bad "mtime"
[ "\$(stat -c %a "$HOME/KyDrive")" = 700 ] && ok "sync root is 0700" || bad "sync root mode"
printf 'from the laptop' >"$HOME/KyDrive/Docs/local.txt"
waitfor 'curl -s -b "$W/cookies" "$BASE/api/drive/workspaces/$WS/snapshot" | grep -q local.txt' && ok "fsnotify upload of local.txt" || bad "upload after local write"
if grep -rlaF "\$TOKEN" "$W/home" "$W/run" "$W/sync.log" 2>/dev/null; then bad "token found on disk"; else ok "token in no file under HOME, runtime dir or log"; fi
ctl Status | grep -q '"State": "idle"' && ok "Status idle" || bad "Status: \$(ctl Status)"
DEV=\$(curl -s -b "$W/cookies" "$BASE/api/devices" | jq -r '.[0].id')
printf 'unuploaded edit' >"$HOME/KyDrive/Docs/hello.txt"
curl -s -o /dev/null -b "$W/cookies" -X DELETE -H "X-CSRF-Token: \$(awk '\$6 == "ky_csrf" { print \$7 }' "$W/cookies")" "$BASE/api/devices/\$DEV?wipe=true"
ctl Pause '{"Paused":false}' >/dev/null
waitfor 'ctl Status | grep -q "\"State\": \"revoked\""' && ok "revoke reaches the core" || bad "not revoked: \$(ctl Status)"
[ ! -e "$HOME/KyDrive/Docs/local.txt" ] && ok "wipe removed the synced copy" || bad "synced copy kept"
[ "\$(cat "$HOME/KyDrive/Unsynced/Docs/hello.txt" 2>/dev/null)" = "unuploaded edit" ] && ok "unuploaded edit kept in Unsynced/" || bad "edit lost: \$(find "$HOME/KyDrive")"
[ -z "\$(secret-tool lookup service KyDrive username "$BASE" 2>/dev/null)" ] && ok "keyring item deleted" || bad "keyring item survived"
ctl Link '{"Server":"$BASE","Name":"smoke laptop"}' >/dev/null && ok "Link again after a revoke" || bad "relink"
TOKEN2=\$(secret-tool lookup service KyDrive username "$BASE" 2>/dev/null || :)
[ "\$(curl -s -b "$W/cookies" "$BASE/api/devices" | jq length)" = 1 ] && ok "server lists the new device" || bad "device list"
ctl Unlink '{"RemoveFiles":false}' >/dev/null && ok "Unlink" || bad "Unlink"
[ "\$(curl -s -b "$W/cookies" "$BASE/api/devices" | jq length)" = 0 ] && ok "Unlink revoked the device on the server" || bad "device still listed"
curl -s -H "Authorization: Bearer \$TOKEN2" "$BASE/api/drive/workspaces" | grep -q '"error":"device_revoked","wipe":false' && ok "old token answers device_revoked without wipe" || bad "old token: \$(curl -s -H "Authorization: Bearer \$TOKEN2" "$BASE/api/drive/workspaces")"
[ -z "\$(secret-tool lookup service KyDrive username "$BASE" 2>/dev/null)" ] && ok "Unlink deleted the keyring item" || bad "keyring item survived Unlink"
[ \$FAIL = 0 ] || tail -20 "$W/sync.log"
kill \$SYNC_PID; wait \$SYNC_PID 2>/dev/null
exit \$FAIL
EOF
