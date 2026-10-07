<div align="center">

# Hami Panel

**A VPN/proxy management panel built for one thing above all: configs that actually work.**

Built by **HAMI SMART SYSTEMS** · License **GPL-3.0** · [فارسی](README.md)

</div>

---

## Distribution model

> **Who is an "operator"?** In this document the operator = the server owner and service seller (the person who installs this panel on their own server and creates configs for their customers) — nothing to do with mobile carriers.

Hami Panel ships **for independent installation on each operator's own server**; this repository does not offer a hosted SaaS or a shared managed panel. Every operator runs their own database and core on their own machine.

## Why Hami Panel?

Today's panels are good — but they share one blind spot: **when they generate a broken link, they
don't notice.** The link looks fine, gets saved, gets delivered to the customer —
and only when the customer calls do we learn that no traffic passes at all.

Documented examples from existing panels:

| Problem | Source |
|---|---|
| Reality settings leak from another inbound into an XHTTP inbound ⇒ a link that is "correct" but doesn't work | [3x-ui #4987](https://github.com/MHSanaei/3x-ui/issues/4987) · [reopened as #6424](https://github.com/MHSanaei/3x-ui/issues/6424) |
| A core update silently breaks Reality; the panel keeps saying "core is healthy" | [3x-ui #5922](https://github.com/MHSanaei/3x-ui/issues/5922) |
| Single point of failure: multi-server means installing several separate panels | [comparison](https://valebyte.com/en/blog/marzban-vs-3x-ui-choosing-your-vpn-panel/) |
| 3x-ui officially states it is for personal use, not production | [comparison](https://smartvds.org/hosting-basics/marzban-vs-3x-ui/) |
| No device count limits, no quota-exhaustion warnings, no speed limiting | [source](https://valebyte.com/en/blog/3x-ui-marzban-how-to-set-user-traffic-limits/) |
| Users have requested multi-server support for years, unanswered | [3x-ui #3031](https://github.com/MHSanaei/3x-ui/issues/3031) |

### The six pillars of Hami Panel

1. **Every config is really tested before delivery.** The panel connects through the very link it made and
   confirms that traffic passes; "saved" is not enough.
2. **Link generation is deterministic and testable.** A link is produced only from the inbound the user is
   actually attached to; there is no global search and no silent merging across inbounds
   (see: `internal/link/link_test.go`).
3. **The core version is pinned** and upgrades roll out in stages with a "canary" test and automatic rollback.
4. **Health lives inside the panel:** port, TLS, real link validity, quota trust — with alerting and
   automatic repair for known issues.
5. **Multi-server from day one:** a lightweight agent per node; the smart sub-link only serves
   nodes that are currently healthy.
6. **Migratable:** direct import from 3x-ui and Marzban, without rebuilding your users.

## Architecture

```
                    ┌───────────────────────────────┐
   Admin browser  ──▶│  Hami Panel  (Go, single binary)│
   Telegram bot   ──▶│  API · Web UI · Scheduler      │
   Customer (sub) ──▶│  Link generation · Config test │
                    └──────────────┬──────────────────┘
                                │  nodes (this server or another)
                    ┌─────────────▼──────────────────┐
                    │  Hami agent (Go, lightweight)  │
                    │  Xray / sing-box · traffic     │
                    └────────────────────────────────┘
```

| Component | Choice | Reason |
|---|---|---|
| Language | Go | single binary, low footprint, network-friendly, close to the Xray ecosystem |
| Database | SQLite | local file, no separate database service |
| Core | Xray (default) + sing-box (Hysteria2 / TUIC / AnyTLS) | covers the implemented protocols |
| UI | HP-UI with self-contained HTML/CSS/JavaScript + API | no CDN or external assets needed |
| Install | local systemd script + one-command backup/restore | independent install on the operator's server |

## Project status

| Area | Status |
|---|---|
| Link generation & validation (vless / vmess / trojan / ss) + tests | ✅ done |
| Sub-link (base64) | ✅ done |
| Database and user/inbound/traffic/node/event models | ✅ done (`internal/store`) |
| Xray process management and link-consistent config generation | ✅ `hami gen` + `internal/xray` |
| **Real config test (canary)** | ✅ `hami canary` — passes traffic using the link itself |
| Port / TLS / link-validity watchdog | ✅ `hami guard` — repairs with the exact same spec only |
| Core version pinning and upgrade rollback | ✅ `hami pin` / `hami upgrade` |
| One-command backup/restore | ✅ `hami backup` / `hami restore` |
| Event audit log | ✅ `hami audit` — guard / canary / backup / restore / upgrade |
| Multi-server & failover | ✅ `hami node` / `hami agent` — unhealthy nodes drop out of sub-links |
| Second core sing-box (Hysteria2 / TUIC / AnyTLS) | ✅ `internal/singbox` + 11 ready templates |
| Web UI (HP-UI) | ✅ `hami web serve` — login, dashboard, bilingual, three themes, zero external files |
| Admin account & sessions | ✅ `hami admin` — PBKDF2, HttpOnly cookie, failed-attempt throttling |
| Self-hosted installer for Linux/systemd | ✅ `scripts/install.sh` — one-line install with prebuilt binary, auto prerequisites on Ubuntu/Debian/CentOS/Alma, loopback panel and pinned Xray with checksum |
| Inbound & user management from the dashboard | ✅ create/edit/delete, batch build, QR code and subscription link |
| In-place inbound editing (no delete-and-recreate) | ✅ done ([d9a82ca](https://github.com/hamismartsystems/Hami_panel/commit/d9a82ca)) |
| **Migration from other panels** | ✅ `hami import` — 3x-ui, x-ui, Marzban; also from the dashboard |
| Public subscription link | ✅ separate service on loopback behind a tunnel — the panel itself stays closed |
| Traffic accounting & quotas | ✅ stats read from the core, collected every minute, quotas that actually run out |
| Online/offline & user details | ✅ last seen, status dot, details pane |
| Automatic application of user changes to the core | ✅ every user or inbound change rebuilds the config and reloads the core |
| **Telegram bot & sales** | ✅ `hami bot serve` — purchase, wallet, card-to-card with admin approval, automatic config + QR delivery |
| Reseller role | ✅ `hami bot reseller add\|remove\|list` — half the customer price |
| Expiry & quota notices | ✅ `hami notice` on a timer |
| Public API & financial reports | ✅ done |

Full roadmap: [docs/ROADMAP.md](docs/ROADMAP.md) · design: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Quick install — one line (recommended)

On Ubuntu / Debian / CentOS / AlmaLinux / Rocky (systemd, amd64 or arm64) — even without Go:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/hamismartsystems/Hami_panel/main/scripts/install.sh)
```

The installer automatically downloads the Hami binary from [Releases](https://github.com/hamismartsystems/Hami_panel/releases/latest) and verifies its SHA-256, then installs the panel (default `127.0.0.1:8080`) and the pinned Xray as systemd services and enables them.

Create your first admin (if you omit the password, a strong one is generated and printed once):

```bash
sudo /opt/hami/hami admin create -db /var/lib/hami/panel.db -user admin
```

The panel intentionally runs on loopback only; reach the dashboard from your own machine via an SSH tunnel:

```bash
ssh -L 8080:127.0.0.1:8080 root@SERVER_IP
# then browse http://127.0.0.1:8080/hp-ui/
```

For permanent public access, put a TLS reverse proxy in front of the panel or pass the certificate to `web serve` directly (see the self-hosted section below).

## Supported operating systems

| Operating system | Status |
|---|---|
| Ubuntu 20.04+ / Debian 11+ | ✅ primary, tested |
| CentOS 7+ / AlmaLinux / Rocky (RHEL family) | ✅ installer self-installs prerequisites via dnf/yum |
| Fedora and other systemd distros | ✅ standard paths; install prerequisites manually if asked |
| Windows Server | ❌ the service model is systemd/Linux — recommended: an Ubuntu VM in Hyper-V |
| pfSense / FreeBSD | ❌ firewall appliance, not an app host — install the panel on a Linux machine beside it |

## Self-hosted install & usage

This repository's installer targets Linux with `systemd` and installs the pinned Xray core version `26.3.27` on `amd64` and `arm64`. Xray is downloaded from the official GitHub release and verified against a fixed SHA-256. The installer does not open any firewall ports by itself.

### Prerequisites

- `root` or `sudo` access and a running `systemd`.
- `curl`, `sha256sum` and `python3` — if missing, the installer adds them with the distro's package manager (apt/dnf/yum).
- Go `1.27` or newer to build the Hami binary. Build on the target host, or cross-build for the same Linux architecture.
- Outbound access to GitHub to fetch the Xray archive.

### Build & install

```bash
git clone https://github.com/hamismartsystems/Hami_panel.git
cd Hami_panel
go build -trimpath -o hami ./cmd/hami
sudo env HAMI_BIN="$PWD/hami" bash ./scripts/install.sh
```

Install defaults:

| Item | Value |
|---|---|
| Panel | `127.0.0.1:8080` loopback only |
| Hami binary | `/opt/hami/hami` |
| Database | `/var/lib/hami/panel.db` |
| Xray config | `/etc/hami/xray.json` |
| Services | `hami-panel.service` and `hami-xray.service`; enabled at boot |

If the default port is taken, pick another free loopback port, e.g.:

```bash
sudo env HAMI_BIN="$PWD/hami" HAMI_LISTEN_ADDR=127.0.0.1:18080 bash ./scripts/install.sh
```

To generate subscription links with a fixed public address, set `HAMI_BASE_URL` during install; in this example replace HTTPS and the domain with your own values:

```bash
sudo env HAMI_BIN="$PWD/hami" HAMI_BASE_URL=https://panel.example.com bash ./scripts/install.sh
```

`HAMI_ENABLE_AT_BOOT=no` starts the services now but does not enable them for boot. The installer does not overwrite existing units or non-empty target directories; if a run stops halfway, inspect the service status and created files first instead of blindly re-running it.

### Login and daily work

After install, create the first admin; the strong password is printed only once — keep it somewhere safe and do not pass it on the command line as `-password`:

```bash
sudo /opt/hami/hami admin create -db /var/lib/hami/panel.db -user admin
```

The panel listens on loopback only, on purpose. From your own machine open an SSH tunnel:

```bash
ssh -N -L 8080:127.0.0.1:8080 <user>@<server>
```

Then browse `http://127.0.0.1:8080/hp-ui/login` on that machine. If you chose a different port during install, use that port in both the tunnel command and the browser address. For internet-facing access keep the backend on loopback and only pass traffic through an HTTPS reverse proxy; the installer itself does not create a reverse proxy or TLS certificates. In that case set `HAMI_BASE_URL` to your public HTTPS URL and proxy the `/sub/` path as well.

To create configs from the dashboard:

1. In **Inbounds** click **New inbound**, pick a template and enter the public address and a free port. Reality templates generate their keys automatically.
2. The chosen port must be usable on the server; open the traffic port separately in your firewall/network policy. The installer does not touch the firewall.
3. The panel builds the Xray config and refreshes the Xray service. Then in **Users** create a user, pick the inbound and set quota/duration/device limits.
4. Hand the subscription link to the user from the dashboard. The link token is like a password — do not publish it. The sub-link updates as the inbound changes.

For service status and error logs:

```bash
sudo systemctl status hami-panel.service hami-xray.service
sudo journalctl -u hami-panel.service -u hami-xray.service -n 100 --no-pager
```

**Scope of this installer:** it only installs Xray and wires it to the local service. sing-box templates (Hysteria2, TUIC and AnyTLS) appear in the template list, but sing-box itself is not installed or managed by this script — configure that separately.

## Run & test (contributors)

```bash
git clone https://github.com/hamismartsystems/Hami_panel
cd hami-panel
go test ./...          # link, database, Xray config — everything must stay green
# live test: without XRAY_BIN this section is skipped, not falsely green
XRAY_BIN=/path/to/xray go test ./internal/xray/ -count=1 -run Canary
go build -o hami ./cmd/hami
./hami gen -spec inbounds.json -out config.json
./hami canary -spec inbounds.json -xray /path/to/xray
./hami guard -spec inbounds.json
```

`inbounds.json` is the same inbound the link is built from, plus `privateKey`
and `dest`, which only belong in the server config. If Reality is half-broken the command
fails and does not hand you a "healthy" file. `hami canary` builds its client
only from the generated link — never from the server's private key.

## License

**GPL-3.0** — anyone who modifies Hami Panel must publish their changes openly too.
Just as we built on the work of others, others may build on ours.

<div align="center">HAMI SMART SYSTEMS</div>
