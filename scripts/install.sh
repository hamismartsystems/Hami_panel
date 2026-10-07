#!/usr/bin/env bash
# Install the Hami Panel binary (prebuilt from GitHub Releases, or a locally built one via HAMI_BIN) and the pinned Xray Core release.
# The panel binds to loopback by default; use a TLS reverse proxy for remote access.
# Supported: Ubuntu, Debian, CentOS, AlmaLinux, Rocky, Fedora (systemd; apt/dnf/yum).
set -Eeuo pipefail

fail() { echo "install: $*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || fail "run as root (sudo)"
command -v systemctl >/dev/null || fail "systemd is required"
OS_ID=""
if [[ -r /etc/os-release ]]; then
  OS_ID=$(. /etc/os-release; echo "${ID:-unknown}")
fi
PKG_MGR=""
if command -v apt-get >/dev/null; then PKG_MGR=apt
elif command -v dnf >/dev/null; then PKG_MGR=dnf
elif command -v yum >/dev/null; then PKG_MGR=yum
fi

# Install missing prerequisites with the distro's own package manager.
need_pkgs=()
command -v curl      >/dev/null || need_pkgs+=(curl)
command -v sha256sum >/dev/null || need_pkgs+=(coreutils)
command -v python3   >/dev/null || need_pkgs+=(python3)
if (( ${#need_pkgs[@]} > 0 )); then
  [[ "${HAMI_SKIP_DEPS:-no}" != "yes" ]] || fail "missing prerequisites: ${need_pkgs[*]} (HAMI_SKIP_DEPS=yes)"
  [[ -n "$PKG_MGR" ]] || fail "missing prerequisites (${need_pkgs[*]}) and no apt/dnf/yum was found; install them manually"
  echo "install: distro=${OS_ID:-unknown}; installing prerequisites with $PKG_MGR: ${need_pkgs[*]}"
  case "$PKG_MGR" in
    apt) apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${need_pkgs[@]}" ;;
    dnf) dnf install -y -q "${need_pkgs[@]}" ;;
    yum) yum install -y -q "${need_pkgs[@]}" ;;
  esac || fail "package installation failed: ${need_pkgs[*]}"
fi
command -v curl      >/dev/null || fail "curl is required"
command -v sha256sum >/dev/null || fail "sha256sum is required"
command -v python3   >/dev/null || fail "python3 is required"

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
HAMI_BIN=${HAMI_BIN:-"$SCRIPT_DIR/../hami"}
HAMI_PREFIX=${HAMI_PREFIX:-/opt/hami}
HAMI_STATE_DIR=${HAMI_STATE_DIR:-/var/lib/hami}
HAMI_CONFIG_DIR=${HAMI_CONFIG_DIR:-/etc/hami}
HAMI_LISTEN_ADDR=${HAMI_LISTEN_ADDR:-127.0.0.1:8080}
HAMI_BASE_URL=${HAMI_BASE_URL:-}
HAMI_PANEL_UNIT=${HAMI_PANEL_UNIT:-hami-panel}
HAMI_XRAY_UNIT=${HAMI_XRAY_UNIT:-hami-xray}
HAMI_TRAFFIC_UNIT=${HAMI_TRAFFIC_UNIT:-hami-traffic}
HAMI_NOTICE_UNIT=${HAMI_NOTICE_UNIT:-hami-notice}
HAMI_ENABLE_AT_BOOT=${HAMI_ENABLE_AT_BOOT:-yes}
HAMI_TRAFFIC_TIMER=${HAMI_TRAFFIC_TIMER:-yes}
HAMI_NOTICE_TIMER=${HAMI_NOTICE_TIMER:-yes}
HAMI_TELEGRAM=${HAMI_TELEGRAM:-}
# Which addresses may state who the visitor is through X-Forwarded-For.
# The panel listens on loopback, so the reverse proxy that terminates TLS
# is on this machine; without trusting it, every visitor would share one
# throttle key and five bad passwords anywhere would lock the operator
# out. "none" trusts nobody and is what to set when the panel is reached
# directly (an SSH tunnel with nothing in front of it).
HAMI_TRUSTED_PROXIES=${HAMI_TRUSTED_PROXIES:-127.0.0.1/8}
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

HAMI_ASSET=""
case "$(uname -m)" in
  x86_64|amd64)  HAMI_ASSET="hami-linux-amd64" ;;
  aarch64|arm64) HAMI_ASSET="hami-linux-arm64" ;;
  *) fail "no prebuilt Hami binary for architecture: $(uname -m); build with Go 1.27+ and set HAMI_BIN" ;;
esac
HAMI_TMP=""
if [[ ! -x "$HAMI_BIN" ]]; then
  # One-line mode (curl | bash or a bare clone): pull the prebuilt release binary.
  HAMI_TMP=$(mktemp -d)
  HAMI_REL="https://github.com/hamismartsystems/Hami_panel/releases/latest/download"
  echo "install: no local binary found; fetching prebuilt $HAMI_ASSET from GitHub Releases"
  curl --fail --location --retry 3 --connect-timeout 20 "$HAMI_REL/$HAMI_ASSET" -o "$HAMI_TMP/$HAMI_ASSET" \
    || fail "could not download the prebuilt Hami binary; build it yourself (see README) and pass HAMI_BIN"
  curl --fail --location --retry 3 --connect-timeout 20 "$HAMI_REL/checksums.txt" -o "$HAMI_TMP/checksums.txt" \
    || fail "could not download the release checksums"
  (cd "$HAMI_TMP" && grep -F "  $HAMI_ASSET" checksums.txt | sha256sum --check --status) \
    || fail "Hami binary SHA-256 mismatch"
  chmod +x "$HAMI_TMP/$HAMI_ASSET" 2>/dev/null || true
  HAMI_BIN="$HAMI_TMP/$HAMI_ASSET"
fi

[[ "$XRAY_VERSION" == 26.3.27 ]] || fail "this installer is pinned to Xray $XRAY_VERSION only"
[[ -x "$HAMI_BIN" ]] || fail "HAMI_BIN must point to a built, executable hami binary"
"$HAMI_BIN" template list >/dev/null || fail "HAMI_BIN is not a working Hami binary"
[[ "$HAMI_LISTEN_ADDR" =~ ^(127\.0\.0\.1|\[::1\]):[0-9]{1,5}$ ]] || fail "bind only to loopback, e.g. 127.0.0.1:8080; put TLS/reverse proxy in front"
LISTEN_PORT=${HAMI_LISTEN_ADDR##*:}
(( 10#$LISTEN_PORT >= 1 && 10#$LISTEN_PORT <= 65535 )) || fail "invalid loopback port: $LISTEN_PORT"
[[ -z "$HAMI_BASE_URL" || ( "$HAMI_BASE_URL" == https://* && "$HAMI_BASE_URL" != *[[:space:]]* ) ]] || fail "HAMI_BASE_URL must be an https:// URL without spaces"
[[ "$HAMI_ENABLE_AT_BOOT" == yes || "$HAMI_ENABLE_AT_BOOT" == no ]] || fail "HAMI_ENABLE_AT_BOOT must be yes or no"
for value in "$HAMI_TRAFFIC_TIMER" "$HAMI_NOTICE_TIMER"; do
  [[ "$value" == yes || "$value" == no ]] || fail "HAMI_TRAFFIC_TIMER and HAMI_NOTICE_TIMER must be yes or no"
done
[[ -z "$HAMI_TELEGRAM" || "$HAMI_TELEGRAM" == *:* ]] || fail "HAMI_TELEGRAM must be BOTTOKEN:CHATID"

if [[ "$HAMI_TRUSTED_PROXIES" != none ]]; then
  python3 - "$HAMI_TRUSTED_PROXIES" <<'PY' || fail "HAMI_TRUSTED_PROXIES must be a comma-separated list of IPs/CIDRs, or the word none"
import ipaddress, sys
for part in sys.argv[1].split(","):
    part = part.strip()
    if not part:
        continue
    ipaddress.ip_network(part, strict=False)
PY
fi

for value in "$HAMI_PREFIX" "$HAMI_STATE_DIR" "$HAMI_CONFIG_DIR"; do
  [[ "$value" == /* && "$value" != *[[:space:]]* ]] || fail "install paths must be absolute and contain no spaces: $value"
done
for unit in "$HAMI_PANEL_UNIT" "$HAMI_XRAY_UNIT" "$HAMI_TRAFFIC_UNIT" "$HAMI_NOTICE_UNIT"; do
  [[ "$unit" =~ ^[A-Za-z0-9_.@-]+$ ]] || fail "invalid systemd unit name: $unit"
done
[[ "$HAMI_PANEL_UNIT" != "$HAMI_XRAY_UNIT" ]] || fail "panel and Xray unit names must differ"
[[ "$HAMI_TRAFFIC_UNIT" != "$HAMI_NOTICE_UNIT" ]] || fail "traffic and notice unit names must differ"

PANEL_FILE="/etc/systemd/system/${HAMI_PANEL_UNIT}.service"
XRAY_FILE="/etc/systemd/system/${HAMI_XRAY_UNIT}.service"
TRAFFIC_FILE="/etc/systemd/system/${HAMI_TRAFFIC_UNIT}.service"
TRAFFIC_TIMER_FILE="/etc/systemd/system/${HAMI_TRAFFIC_UNIT}.timer"
NOTICE_FILE="/etc/systemd/system/${HAMI_NOTICE_UNIT}.service"
NOTICE_TIMER_FILE="/etc/systemd/system/${HAMI_NOTICE_UNIT}.timer"
for file in "$PANEL_FILE" "$XRAY_FILE" "$TRAFFIC_FILE" "$TRAFFIC_TIMER_FILE" \
            "$NOTICE_FILE" "$NOTICE_TIMER_FILE"; do
  [[ ! -e "$file" ]] || fail "target systemd unit already exists: $file; refusing to overwrite"
done
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
trap 'rm -rf -- "$TMP_DIR"; [[ -z "${HAMI_TMP:-}" ]] || rm -rf -- "$HAMI_TMP"' EXIT
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
PROXIES_ARG=""
if [[ "$HAMI_TRUSTED_PROXIES" != none ]]; then
  PROXIES_ARG="-trusted-proxies $HAMI_TRUSTED_PROXIES"
fi
cat >"$PANEL_FILE" <<UNIT
[Unit]
Description=Hami Panel
After=network-online.target $HAMI_XRAY_UNIT.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$HAMI_STATE_DIR
ExecStart=$HAMI_PREFIX/hami web serve -db $HAMI_STATE_DIR/panel.db -addr $HAMI_LISTEN_ADDR $BASE_URL_ARG $PROXIES_ARG -xray-config $HAMI_CONFIG_DIR/xray.json -reload-cmd "systemctl restart $HAMI_XRAY_UNIT.service"
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

# The panel is complete without these, but not finished: nothing else
# depletes a quota or ends a term, so an installation without a collector
# looks healthy while every subscription quietly never runs out. They are
# installed by default and can be turned off with HAMI_TRAFFIC_TIMER=no /
# HAMI_NOTICE_TIMER=no for an operator who runs collection elsewhere.
NOTICE_TELEGRAM_FLAG=""
if [[ -n "$HAMI_TELEGRAM" ]]; then
  NOTICE_TELEGRAM_FLAG="-telegram $HAMI_TELEGRAM"
fi

write_traffic_units() {
cat >"$TRAFFIC_FILE" <<UNIT
[Unit]
Description=Hami: count traffic and end finished subscriptions
After=network-online.target $HAMI_XRAY_UNIT.service
Wants=network-online.target

[Service]
Type=oneshot
# Exiting 1 means "the core did not answer", which is ordinary while it
# restarts; the detail goes to the panel's own event log and the next
# minute simply tries again.
SuccessExitStatus=1
ExecStart=$HAMI_PREFIX/hami traffic collect -db $HAMI_STATE_DIR/panel.db -xray $HAMI_STATE_DIR/xray/xray -quiet -config $HAMI_CONFIG_DIR/xray.json -reload-cmd "systemctl restart $HAMI_XRAY_UNIT.service"
UMask=0077
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$HAMI_STATE_DIR $HAMI_CONFIG_DIR
UNIT
chmod 0644 "$TRAFFIC_FILE"
cat >"$TRAFFIC_TIMER_FILE" <<UNIT
[Unit]
Description=Hami: collect traffic every minute

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=10s
Unit=$HAMI_TRAFFIC_UNIT.service

[Install]
WantedBy=timers.target
UNIT
chmod 0644 "$TRAFFIC_TIMER_FILE"
}

write_notice_units() {
cat >"$NOTICE_FILE" <<UNIT
[Unit]
Description=Hami: warn customers who are expiring or nearly out of quota
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$HAMI_PREFIX/hami notice -db $HAMI_STATE_DIR/panel.db -days 3 -ratio 0.8 $NOTICE_TELEGRAM_FLAG
UMask=0077
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$HAMI_STATE_DIR
UNIT
chmod 0644 "$NOTICE_FILE"
cat >"$NOTICE_TIMER_FILE" <<UNIT
[Unit]
Description=Hami: daily expiry and quota warnings

[Timer]
OnBootSec=5min
OnCalendar=daily
Persistent=true
Unit=$HAMI_NOTICE_UNIT.service

[Install]
WantedBy=timers.target
UNIT
chmod 0644 "$NOTICE_TIMER_FILE"
}

TRAFFIC_TIMER_ENABLED=no
NOTICE_TIMER_ENABLED=no
if [[ "$HAMI_TRAFFIC_TIMER" == yes ]]; then
  write_traffic_units
  TRAFFIC_TIMER_ENABLED=yes
fi
if [[ "$HAMI_NOTICE_TIMER" == yes ]]; then
  write_notice_units
  NOTICE_TIMER_ENABLED=yes
fi

systemctl daemon-reload
TIMER_UNITS=()
[[ "$TRAFFIC_TIMER_ENABLED" == yes ]] && TIMER_UNITS+=("$HAMI_TRAFFIC_UNIT.timer")
[[ "$NOTICE_TIMER_ENABLED" == yes ]] && TIMER_UNITS+=("$HAMI_NOTICE_UNIT.timer")

if [[ "$HAMI_ENABLE_AT_BOOT" == yes ]]; then
  systemctl enable --now "$HAMI_XRAY_UNIT.service" "$HAMI_PANEL_UNIT.service"
  if (( ${#TIMER_UNITS[@]} )); then
    systemctl enable --now "${TIMER_UNITS[@]}"
  fi
else
  systemctl start "$HAMI_XRAY_UNIT.service" "$HAMI_PANEL_UNIT.service"
  if (( ${#TIMER_UNITS[@]} )); then
    systemctl start "${TIMER_UNITS[@]}"
  fi
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
if [[ "$HAMI_TRUSTED_PROXIES" != none ]]; then
  cat <<EOF
X-Forwarded-For is believed only from $HAMI_TRUSTED_PROXIES. If nothing sits in
front of the panel (no reverse proxy, no nginx), reinstall with
HAMI_TRUSTED_PROXIES=none so the throttle cannot be fooled by a header.
EOF
fi
if [[ "$TRAFFIC_TIMER_ENABLED" == yes ]]; then
  cat <<EOF
Traffic counters are collected every minute ($HAMI_TRAFFIC_UNIT.timer): that is
what makes quotas and end dates real, and what the dashboard's usage and
online figures come from. Watch it with:
  systemctl list-timers $HAMI_TRAFFIC_UNIT.timer
  journalctl -u $HAMI_TRAFFIC_UNIT.service -n 50 --no-pager
EOF
else
  cat <<EOF
HAMI_TRAFFIC_TIMER=no: nothing is counting traffic or ending subscriptions.
Run this from your own scheduler, once a minute:
  $HAMI_PREFIX/hami traffic collect -db $HAMI_STATE_DIR/panel.db \
      -xray $HAMI_STATE_DIR/xray/xray -config $HAMI_CONFIG_DIR/xray.json \
      -reload-cmd "systemctl restart $HAMI_XRAY_UNIT.service"
Without -config and -reload-cmd a switch-off stays in the database and the
customer keeps connecting until the panel next writes the config.
EOF
fi
if [[ "$NOTICE_TIMER_ENABLED" == yes && -z "$HAMI_TELEGRAM" ]]; then
  cat <<EOF
Warnings run daily ($HAMI_NOTICE_UNIT.timer) and are written to the panel's
event log. Set HAMI_TELEGRAM=BOTTOKEN:CHATID and reinstall to also push them
to a Telegram chat.
EOF
fi
