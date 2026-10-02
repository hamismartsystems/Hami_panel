#!/usr/bin/env bash
# Install a locally built Hami Panel binary and the pinned Xray Core release.
# The panel binds to loopback by default; use a TLS reverse proxy for remote access.
set -Eeuo pipefail

fail() { echo "install: $*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || fail "run as root (sudo)"
command -v systemctl >/dev/null || fail "systemd is required"
command -v curl >/dev/null || fail "curl is required"
command -v sha256sum >/dev/null || fail "sha256sum is required"
command -v python3 >/dev/null || fail "python3 is required"

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
HAMI_BIN=${HAMI_BIN:-"$SCRIPT_DIR/../hami"}
HAMI_PREFIX=${HAMI_PREFIX:-/opt/hami}
HAMI_STATE_DIR=${HAMI_STATE_DIR:-/var/lib/hami}
HAMI_CONFIG_DIR=${HAMI_CONFIG_DIR:-/etc/hami}
HAMI_LISTEN_ADDR=${HAMI_LISTEN_ADDR:-127.0.0.1:8080}
HAMI_BASE_URL=${HAMI_BASE_URL:-}
HAMI_PANEL_UNIT=${HAMI_PANEL_UNIT:-hami-panel}
HAMI_XRAY_UNIT=${HAMI_XRAY_UNIT:-hami-xray}
HAMI_ENABLE_AT_BOOT=${HAMI_ENABLE_AT_BOOT:-yes}
XRAY_VERSION=${XRAY_VERSION:-26.3.27}

case "$(uname -m)" in
  x86_64|amd64)
    XRAY_ASSET=Xray-linux-64.zip
    XRAY_SHA256=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae
    ;;
  aarch64|arm64)
    XRAY_ASSET=Xray-linux-arm64-v8a.zip
    XRAY_SHA256=4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c
    ;;
  *) fail "unsupported architecture: $(uname -m) (supported: amd64, arm64)" ;;
esac

[[ "$XRAY_VERSION" == 26.3.27 ]] || fail "this installer is pinned to Xray $XRAY_VERSION only"
[[ -x "$HAMI_BIN" ]] || fail "HAMI_BIN must point to a built, executable hami binary"
"$HAMI_BIN" template list >/dev/null || fail "HAMI_BIN is not a working Hami binary"
[[ "$HAMI_LISTEN_ADDR" =~ ^(127\.0\.0\.1|\[::1\]):[0-9]{1,5}$ ]] || fail "bind only to loopback, e.g. 127.0.0.1:8080; put TLS/reverse proxy in front"
LISTEN_PORT=${HAMI_LISTEN_ADDR##*:}
(( 10#$LISTEN_PORT >= 1 && 10#$LISTEN_PORT <= 65535 )) || fail "invalid loopback port: $LISTEN_PORT"
[[ -z "$HAMI_BASE_URL" || ( "$HAMI_BASE_URL" == https://* && "$HAMI_BASE_URL" != *[[:space:]]* ) ]] || fail "HAMI_BASE_URL must be an https:// URL without spaces"
[[ "$HAMI_ENABLE_AT_BOOT" == yes || "$HAMI_ENABLE_AT_BOOT" == no ]] || fail "HAMI_ENABLE_AT_BOOT must be yes or no"

for value in "$HAMI_PREFIX" "$HAMI_STATE_DIR" "$HAMI_CONFIG_DIR"; do
  [[ "$value" == /* && "$value" != *[[:space:]]* ]] || fail "install paths must be absolute and contain no spaces: $value"
done
for unit in "$HAMI_PANEL_UNIT" "$HAMI_XRAY_UNIT"; do
  [[ "$unit" =~ ^[A-Za-z0-9_.@-]+$ ]] || fail "invalid systemd unit name: $unit"
done
[[ "$HAMI_PANEL_UNIT" != "$HAMI_XRAY_UNIT" ]] || fail "panel and Xray unit names must differ"

PANEL_FILE="/etc/systemd/system/${HAMI_PANEL_UNIT}.service"
XRAY_FILE="/etc/systemd/system/${HAMI_XRAY_UNIT}.service"
[[ ! -e "$PANEL_FILE" && ! -e "$XRAY_FILE" ]] || fail "one of the target systemd units already exists; refusing to overwrite"
for dir in "$HAMI_PREFIX" "$HAMI_STATE_DIR" "$HAMI_CONFIG_DIR"; do
  if [[ -e "$dir" ]]; then
    [[ -d "$dir" ]] || fail "target path exists and is not a directory: $dir"
    [[ -z "$(find "$dir" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail "target directory is not empty: $dir; refusing to overwrite"
  fi
done
[[ ! -e "$HAMI_PREFIX/hami" ]] || fail "Hami binary already exists at $HAMI_PREFIX/hami; refusing to overwrite"
[[ ! -e "$HAMI_STATE_DIR/panel.db" ]] || fail "database already exists at $HAMI_STATE_DIR/panel.db; refusing to overwrite"
[[ ! -e "$HAMI_CONFIG_DIR/xray.json" ]] || fail "Xray config already exists at $HAMI_CONFIG_DIR/xray.json; refusing to overwrite"

TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT
XRAY_CANDIDATE="$TMP_DIR/xray"

if [[ -n "${XRAY_BIN:-}" ]]; then
  [[ -x "$XRAY_BIN" ]] || fail "XRAY_BIN is not executable"
  install -m 0755 "$XRAY_BIN" "$XRAY_CANDIDATE"
else
  XRAY_URL="https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${XRAY_ASSET}"
  XRAY_ZIP="$TMP_DIR/$XRAY_ASSET"
  curl --fail --location --retry 3 --connect-timeout 20 "$XRAY_URL" -o "$XRAY_ZIP"
  printf '%s  %s\n' "$XRAY_SHA256" "$XRAY_ZIP" | sha256sum --check --status || fail "Xray archive SHA-256 mismatch"
  python3 - "$XRAY_ZIP" "$XRAY_CANDIDATE" <<'PY'
import pathlib, sys, zipfile
archive, target = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
with zipfile.ZipFile(archive) as z:
    entries = [n for n in z.namelist() if pathlib.PurePosixPath(n).name == "xray" and not n.endswith("/")]
    if len(entries) != 1:
        raise SystemExit("expected exactly one xray executable in the official archive")
    info = z.getinfo(entries[0])
    if info.file_size <= 0 or info.file_size > 100_000_000:
        raise SystemExit("unexpected xray binary size")
    target.write_bytes(z.read(info))
target.chmod(0o755)
PY
fi

XRAY_VERSION_OUTPUT=$("$XRAY_CANDIDATE" version 2>&1) || fail "Xray candidate failed to run"
grep -Fq "$XRAY_VERSION" <<<"$XRAY_VERSION_OUTPUT" || fail "Xray candidate did not report version $XRAY_VERSION"

install -d -m 0755 "$HAMI_PREFIX" "$HAMI_STATE_DIR" "$HAMI_CONFIG_DIR"
install -m 0755 "$HAMI_BIN" "$HAMI_PREFIX/hami"
"$HAMI_PREFIX/hami" upgrade -dir "$HAMI_STATE_DIR/xray" -bin "$XRAY_CANDIDATE" -version "$XRAY_VERSION"

if [[ ! -e "$HAMI_CONFIG_DIR/xray.json" ]]; then
  cat >"$HAMI_CONFIG_DIR/xray.json" <<'JSON'
{
  "log": {"loglevel": "warning"},
  "inbounds": [],
  "outbounds": [{"protocol": "freedom", "tag": "direct"}]
}
JSON
  chmod 0600 "$HAMI_CONFIG_DIR/xray.json"
fi
"$HAMI_STATE_DIR/xray/xray" run -test -c "$HAMI_CONFIG_DIR/xray.json" >/dev/null

cat >"$XRAY_FILE" <<UNIT
[Unit]
Description=Hami-managed Xray Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$HAMI_STATE_DIR/xray
ExecStart=$HAMI_STATE_DIR/xray/xray run -config $HAMI_CONFIG_DIR/xray.json
Restart=on-failure
RestartSec=3
UMask=0077
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$HAMI_CONFIG_DIR

[Install]
WantedBy=multi-user.target
UNIT
chmod 0644 "$XRAY_FILE"

BASE_URL_ARG=""
if [[ -n "$HAMI_BASE_URL" ]]; then
  BASE_URL_ARG="-base-url $HAMI_BASE_URL"
fi
cat >"$PANEL_FILE" <<UNIT
[Unit]
Description=Hami Panel
After=network-online.target $HAMI_XRAY_UNIT.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$HAMI_STATE_DIR
ExecStart=$HAMI_PREFIX/hami web serve -db $HAMI_STATE_DIR/panel.db -addr $HAMI_LISTEN_ADDR $BASE_URL_ARG -xray-config $HAMI_CONFIG_DIR/xray.json -reload-cmd "systemctl restart $HAMI_XRAY_UNIT.service"
Restart=on-failure
RestartSec=3
UMask=0077
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$HAMI_STATE_DIR $HAMI_CONFIG_DIR

[Install]
WantedBy=multi-user.target
UNIT
chmod 0644 "$PANEL_FILE"

systemctl daemon-reload
if [[ "$HAMI_ENABLE_AT_BOOT" == yes ]]; then
  systemctl enable --now "$HAMI_XRAY_UNIT.service" "$HAMI_PANEL_UNIT.service"
else
  systemctl start "$HAMI_XRAY_UNIT.service" "$HAMI_PANEL_UNIT.service"
fi

HEALTH_OK=no
for ((attempt = 1; attempt <= 30; attempt++)); do
  if curl --fail --silent "http://$HAMI_LISTEN_ADDR/hp-ui/login" -o /dev/null 2>/dev/null; then
    HEALTH_OK=yes
    break
  fi
  sleep 1
done
[[ "$HEALTH_OK" == yes ]] || fail "services started but the login page health check failed; inspect journalctl -u $HAMI_PANEL_UNIT"

cat <<EOF
Hami installation completed.
Panel: http://$HAMI_LISTEN_ADDR/hp-ui/login (loopback only; configure HTTPS reverse proxy for remote access)
Xray: $HAMI_STATE_DIR/xray/xray ($XRAY_VERSION)
Database: $HAMI_STATE_DIR/panel.db
Create the first admin (save the generated password once):
  $HAMI_PREFIX/hami admin create -db $HAMI_STATE_DIR/panel.db -user admin
EOF
