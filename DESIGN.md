# PK95 Monitoring – Design and contracts

Monitoring server for one customer per Docker container. Single Go binary with SQLite and an
embedded vanilla-JS SPA in the PK95 Core design (copied from `~/code/pk95proxbackup`).
Monitors **Proxmox VE** (REST API), **Linux/Windows hosts** (pk95agent, push over HTTPS),
**SNMP devices** (switches incl. UniFi, UPS, printers, NAS), **UPS on a NUT server** (Network UPS
Tools, e.g. UniFi UPS) and **network checks** (ping, HTTP(S), TLS certificate, TCP port, DNS). An agent
can be attached to a device polled over API/SNMP (one device, data merged; see "Attached agents"). A central instance can later read every container over the
**external API** (bearer token) or receive a periodic push of the summary.

Code is always English (identifiers, comments, file names, UI routes `/` (overview), `/devices`
(`?state=`, `?group=`), `/devices/<id>`, `/alerts`, `/events`, `/agents`, `/settings`, JSON keys,
log messages). Only texts shown to end users are German (UI, notifications, check names and
messages, API error and validation messages). Unknown page paths serve `index.html`; the SPA shows
the overview for them. Shared types: `internal/model/model.go` (read it first – it is the contract).

## Packages and ownership

| Path | Content |
|---|---|
| `main.go` | server entry: env → `app.Config` → `app.Run` |
| `cmd/pk95agent/` | agent entry (CLI: install, uninstall, run, once, version) |
| `internal/model` | shared types (no deps) |
| `internal/store` | SQLite: settings, objects (JSON docs), users, sessions, events, metrics, rollups, alerts, statuses, API tokens, agent tokens |
| `internal/notify` | e-mail / Telegram / ntfy / Gotify / webhook (copied from ProxBackup, done) |
| `internal/collect/proxmox` | Proxmox VE API client + collector |
| `internal/collect/snmp` | SNMP collector (gosnmp); profiles incl. `unifi`, switch MIBs (LLDP, PoE) |
| `internal/collect/nut` | NUT client + collector (upsd protocol, TCP 3493) |
| `internal/collect/netcheck` | ping, HTTP, TLS, TCP, DNS |
| `internal/agent` | agent runtime (collect, push, buffer, self-update, service) |
| `internal/agentdist` | server side of agent distribution: install scripts (embedded templates), binary catalog with SHA-256 |
| `internal/core` | scheduler, threshold evaluation, hysteresis, alerts, notifications, agent ingestion, rollup/retention, central push, summary |
| `internal/api` | HTTP: session auth, JSON API for the UI, agent endpoints, external API v1 |
| `internal/app` | wiring, TLS (self-signed or cert.pem/key.pem), setup code |
| `internal/fake` | demo mode (`PK95MON_DEMO=1`): wires simulated PVE/SNMP/agents into core |
| `internal/fake/fakepve` | simulated Proxmox VE API (httptest TLS server) – used by tests and demo |
| `internal/fake/fakesnmp` | simulated SNMP devices (implements the snmp package's session interface), incl. a UniFi USW-Pro-24-PoE |
| `internal/fake/fakenut` | simulated NUT server (upsd on 127.0.0.1) – used by tests and demo |
| `web/` | SPA: `index.html`, `app.js`, `style.css` (copied), `theme.js`, `logo.svg`, `fonts/` |
| `Dockerfile`, `docker-compose.yml` | image (linux/amd64, linux/arm64) with the server and the agent binaries for linux/amd64, linux/arm64, windows/amd64 |
| `Makefile` | `test`, `vet`, `fmt`, `check` (vet incl. the agent's per-OS files, gofmt, `node --check`), `build`, `agents` (→ `dist/agents/`), `demo`, `docker` |
| `.github/workflows/release.yml` | CI on push/PR (check, test, agent cross-build, image build); tag `v*`: push image to ghcr.io, GitHub release with agent binaries + `SHA256SUMS` |

`internal/fake/fakepve`, `fakesnmp` and `fakenut` must NOT import `internal/core` (the collectors' tests import them).

## Environment (server)

| Variable | Default | Meaning |
|---|---|---|
| `PK95MON_LISTEN` | `:8443` | HTTPS (or HTTP with NO_TLS) listener |
| `PK95MON_HTTP_REDIRECT` | `` | plain HTTP listener (e.g. `:8080`) that redirects to HTTPS; empty = off. Requests with `X-Forwarded-Proto: https` (or only `X-Forwarded-For`) are served instead of redirected |
| `PK95MON_DATA_DIR` | `/data` | SQLite DB, cert.pem/key.pem, setup-code |
| `PK95MON_AGENT_DIR` | `/opt/pk95monitoring/agents` | agent binaries `pk95agent-linux-amd64`, `pk95agent-linux-arm64`, `pk95agent-windows-amd64.exe` |
| `PK95MON_PUBLIC_URL` | `` | URL agents use to reach the server; env wins over the setting `general.public_url`; both empty = from the request (scheme and `Host`; `X-Forwarded-Proto`/`X-Forwarded-Host` only from a loopback/private peer). Its host also goes into the self-signed certificate |
| `PK95MON_INSTANCE` | `` | instance name (customer); editable in the settings, env wins if set |
| `PK95MON_DEMO` | `` | `1` = simulated devices |
| `PK95MON_NO_TLS` | `` | `1` = plain HTTP (behind a reverse proxy); `PK95MON_HTTP_REDIRECT` is then ignored |

Boolean variables accept `1`, `true`, `yes`, `on` (`main.go` `flag()`). Defaults are the same in
`main.go` and the Docker image (`Dockerfile` `ENV`).

TLS: `cert.pem`/`key.pem` in the data dir (self-signed ECDSA certificate created on first start).
When the certificate is not trusted by the system CAs, the agent commands pin it (`fingerprint` =
SHA-256 of the leaf, curl `--pinnedpubkey` = SHA-256 of the SPKI); with a CA certificate they
rely on normal verification. With `PK95MON_NO_TLS=1` there is no fingerprint at all.

Reverse proxy (recommended when e.g. Caddy terminates TLS): `PK95MON_NO_TLS=1`,
`PK95MON_LISTEN=:8080`, port published on `127.0.0.1` only, `PK95MON_PUBLIC_URL=https://<proxy
host>`. The proxy forwards everything incl. `/agent/`. `X-Forwarded-*` headers are trusted only
from a loopback/private peer (`api.fromProxy`): without TLS the rate limits take the client from
the last `X-Forwarded-For` entry, and the session cookie is `Secure` when
`X-Forwarded-Proto: https`.

## Docker

- Image: Alpine, `/usr/local/bin/pk95monitoring`, agents in `/opt/pk95monitoring/agents`, runs as
  uid/gid `10001`; `/data` is created with that owner, so a new named volume inherits it (a bind
  mount must be `chown 10001:10001` by hand). `VOLUME /data`, `EXPOSE 8443`.
- Build needs BuildKit (`RUN --mount=type=cache`, `COPY <<'EOF'` heredoc for the healthcheck);
  on Debian's `docker.io` install `docker-buildx`, else `docker build`/`docker compose build`
  use the legacy builder and fail. Go stages run on `$BUILDPLATFORM` and cross-compile.
- Healthcheck `/usr/local/bin/pk95monitoring-healthcheck`: busybox `wget` on
  `/api/session` of the local listener, host/port from `PK95MON_LISTEN`, `http` when
  `PK95MON_NO_TLS` is set (same values as `main.go`), else `https` without certificate check.
- Ping as non-root needs unprivileged ICMP sockets: compose sets
  `net.ipv4.ping_group_range: "0 65535"`. Not `"0 2147483647"`: in an unprivileged LXC (Docker
  inside a Proxmox container) only 65536 IDs are mapped and writing the wider range fails with
  "invalid argument", so the container does not start. Not possible with `network_mode: host`.
- Docker inside a Proxmox LXC needs the container features `nesting=1,keyctl=1`.

## Check keys

Stable keys, unique per device. Collectors and agent MUST use these patterns.

| Source | Key | Name (German) | Group |
|---|---|---|---|
| all | `reach` | Erreichbarkeit | Status – synthesized by core when a poll fails / agent silent |
| all except net | `discovery` | Dienste-Inventur | Status – synthesized by core from the inventory: warn while there are new or vanished checks ("2 neue Dienste: …; 1 Dienst verschwunden: …"), else ok ("42 Dienste bekannt, nichts zu entscheiden"); no metrics |
| agent | `system` | System | System (facts: Betriebssystem, Kernel, Hostname, Architektur, CPU, Kerne, Arbeitsspeicher, IP-Adressen, Agent-Version, Laufzeit; metrics `uptime`, `procs`) |
| agent | `cpu` | CPU | System |
| agent | `mem` | Arbeitsspeicher | System |
| agent | `swap` | Auslagerung | System |
| agent | `disk:<mount>` | Festplatte <mount> (`C:` on Windows) | Festplatten |
| agent | `if:<name>` | Netzwerk <name> | Netzwerk |
| agent | `svc:<name>` | Dienst <display name> | Dienste |
| agent | `services` | Dienste | Dienste (Windows: service list could not be read, unknown) |
| agent | `systemd` | Systemd-Units | Dienste (Linux: failed units) |
| agent | `updates` | Updates | Updates (Linux apt or dnf / Windows Update; reboot required = warn) |
| agent | `eventlog` | Ereignisprotokoll | System (Windows; critical events in the last hour = warn) |
| proxmox | `pve:cluster` | Cluster | Cluster (quorum, nodes online) |
| proxmox | `pve:node:<node>` | Knoten <node> | Knoten |
| proxmox | `pve:vm:<vmid>` / `pve:ct:<vmid>` | VM <vmid> <name> / CT … | VMs / Container |
| proxmox | `pve:storage:<node>:<storage>` | Speicher <storage> (<node>) | Speicher (shared storage once, under one node) |
| proxmox | `pve:zfs:<node>:<pool>` | ZFS <pool> (<node>) | Speicher (health from `/disks/zfs`; `/disks/zfs/<pool>` adds vdevs not ONLINE, read/write/checksum errors, data errors = crit, resilver = warn, scrub progress/age/errors) |
| proxmox | `pve:disk:<node>:<dev>` | Platte <dev> (<node>) | Platten (SMART health from `/disks/list`; `/disks/smart`, cached 30 min since smartctl may wake disks: ATA attributes 194/190 temp, 5 reallocated, 197 pending, 198+187 uncorrectable, 199 CRC, 9 hours, 12 cycles, FAILING_NOW = crit; NVMe health log: temp, spare, percentage used, media errors, critical warning bits = crit; warn/crit of the counters by the `disk.*` thresholds) |
| proxmox | `pve:repl:<job>` | Replikation <vmid> <name> → <target> | Replikation (`/nodes/<n>/replication` of the source node: fail_count > 0 = crit with the error) |
| proxmox | `pve:ha` | Hochverfügbarkeit | Cluster (only with HA resources: a resource in error = crit, fence/recovery = warn, no quorum = crit) |
| proxmox | `pve:ceph` | Ceph | Speicher (only when Ceph answers `/cluster/ceph/status`: HEALTH_OK/WARN/ERR with the check summaries) |
| proxmox | `pve:cert:<node>` | Zertifikat <node> | Zertifikate (`/certificates/info`, cached 6 h: pveproxy-ssl.pem, else pve-ssl.pem) |
| proxmox | `pve:updates:<node>` | Updates <node> | Updates |
| proxmox | `pve:backup` | Sicherungen | Sicherungen (failed vzdump tasks last 24 h = crit until a later run backed the guest up) |
| snmp | `snmp:system` | System | System (facts: Name, Beschreibung, Standort, Kontakt, Profil, Laufzeit, plus Modell, Seriennummer (printer, UPS, NAS, UniFi), Firmware (UniFi), DSM-Version (Synology) from the profile; metric `uptime`) |
| snmp | `snmp:cpu`, `snmp:mem` | CPU / Arbeitsspeicher | System |
| snmp | `snmp:storage:<descr>` | Speicher <descr> | Speicher |
| snmp | `snmp:if:<ifName>` | Port <ifName> | Schnittstellen (all interfaces except loopback, at most 512; no link = ok "Kein Link", admin down = ok "Deaktiviert"; only ports in `SNMPConfig.Interfaces` – UI "Alarm bei Link-Ausfall" – are crit when not up). Switch-like profiles (`switch`, `unifi`) add facts "Nachbar" (LLDP remote system name, else chassis id), "Nachbar-Port" (remote port id/description), "PoE" (`aktiv · 6,2 W` / `aktiv` / `aus` / `Fehler`), "PoE-Klasse" (when delivering), "PoE-Fehler"; and hide the FASTPATH "CPU Interface" and idle virtual rows (ifType 1/22/53/131/161 when not up; `unifi` also notPresent) unless watched |
| snmp | `snmp:poe` | PoE | PoE (switch-like profiles, when POWER-ETHERNET-MIB or the vendor PoE table answers: `poe.power`, `poe.budget`, `poe.usage`; crit when the PSE reports a fault, warn when a port has a PoE fault) |
| snmp | `snmp:fan:<name>` | Lüfter <n> | Sensoren (UniFi `Lüfter N`; metric `fan.rpm`, instance name; failed = crit "Lüfter ausgefallen") |
| snmp | `snmp:psu:<name>` | Netzteil <n> | Sensoren (UniFi `Netzteil N`; failed = crit, no input power = ok "Keine Eingangsspannung" because unused RPS inputs report it) |
| snmp | `snmp:ups` | USV | USV (battery temperature as metric `temp`, instance `Batterie`) |
| snmp | `snmp:printer` / `snmp:toner:<descr>` | Drucker / `<descr>`, or `Toner <descr>` when the description does not contain "Toner" | Drucker |
| snmp | `snmp:temp:<name>` | Temperatur <name> | Sensoren (Synology `System`; UniFi `Sensor N`, or `System` when only the unit temperature exists) |
| snmp | `snmp:disk:<diskID>` | Platte <id> | Platten (Synology; metric `temp`, instance diskID) |
| snmp | `snmp:raid:<name>` | RAID <name> | Speicher (Synology) |
| snmp | `snmp:custom:<name>` | <name> | Eigene Werte |
| nut | `nut:<ups>` | the UPS description from LIST UPS, else "USV <ups>" | USV (state from `ups.status`, see "NUT"; facts Hersteller, Modell, Seriennummer, Firmware, Akku-Datum, Treiber, Status (raw flags), USV-Name (when a description is shown)) |
| net | `net:<id>` | NetCheck.Name or e.g. "Ping 10.0.0.1" | Netzwerk-Checks |

## Metrics

`Metric{Name, Instance, Value, Unit}`. Rates (B/s) are computed by the collector from counters
(keep the previous counter per device+instance; skip the first sample and counter wraps).

| Name | Unit | Instance | Source |
|---|---|---|---|
| `cpu.usage` | % | node name for PVE, empty for agent/snmp | agent, pve node, snmp |
| `cpu.load1` | "" | (node for PVE) | agent (Linux), pve node |
| `mem.usage` | % | as cpu | agent, pve node, snmp |
| `mem.used`, `mem.total` | B | | agent, pve node, snmp |
| `swap.usage` | % | (node for PVE) | agent, pve node |
| `disk.usage` | % | mount (`/`, `C:`), PVE node root: node name | agent, pve node, snmp hrStorage |
| `disk.used`, `disk.total` | B | mount / hrStorage name | agent, snmp |
| `net.rx`, `net.tx` | B/s | interface | agent |
| `uptime` | s | (node for PVE) | agent, pve node, snmp |
| `procs` | "" | | agent |
| `services.failed` | "" | | agent |
| `updates.pending`, `updates.security` | "" | (node for PVE) | agent, pve |
| `eventlog.errors` | "" | | agent Windows (System+Application Error/Critical in the last 24 h) |
| `guest.cpu` | % | `vm/<id>` / `ct/<id>` | pve (100 % = all assigned cores) |
| `guest.mem` | % | ditto | pve |
| `guest.mem_used` | B | ditto | pve |
| `guest.netin`, `guest.netout`, `guest.diskread`, `guest.diskwrite` | B/s | ditto | pve |
| `storage.usage` | % | `<node>/<storage>`, `ceph` | pve |
| `cpu.iowait` | % | `<node>` | pve (IO delay of `/nodes/<n>/status` `wait`) |
| `disk.temp` | °C | `<node>/<dev>` | pve SMART |
| `disk.wearout` | % | ditto | pve (used life: 100 − PVE wearout, NVMe "Percentage Used") |
| `disk.spare` | % | ditto | pve NVMe available spare |
| `disk.reallocated`, `disk.pending`, `disk.uncorrectable`, `disk.crc_errors` | "" | ditto | pve ATA SMART raw values |
| `disk.media_errors` | "" | ditto | pve NVMe |
| `zfs.usage`, `zfs.frag` | % | `<node>/<pool>` | pve |
| `zfs.errors` | "" | ditto | pve (sum of vdev read/write/checksum errors) |
| `zfs.scrub_age` | d | ditto | pve (days since the last finished scrub) |
| `repl.age` | min | `<job>` | pve (minutes since the last sync) |
| `storage.used`, `storage.total` | B | ditto | pve |
| `ping.rtt` | ms | | netcheck |
| `ping.loss` | % | | netcheck |
| `http.time` | ms | | netcheck |
| `http.status` | "" | | netcheck |
| `cert.days` | d | (pve: `<node>`) | netcheck http(s)/tls, pve |
| `tcp.time`, `dns.time` | ms | | netcheck |
| `if.rx`, `if.tx` | B/s | ifName | snmp (ports that are up) |
| `if.errors` | 1/s | ifName | snmp (in+out errors, ports that are up) |
| `ups.battery` | % | (nut: `<ups>` when one device polls several UPS) | snmp, nut (`battery.charge`) |
| `ups.runtime` | min | ditto | snmp, nut (`battery.runtime`/60) |
| `ups.load` | % | ditto | snmp, nut |
| `ups.input`, `ups.output` | V | ditto | snmp, nut (`input.voltage`, `output.voltage`) |
| `ups.power` | W | ditto | nut (`ups.realpower`, else `ups.realpower.nominal` × load / 100) |
| `ups.battery_voltage` | V | ditto | nut (`battery.voltage`) |
| `temp` | °C | `System`, Synology diskID, `Batterie` (UPS), `USV` (nut `ups.temperature`; several UPS: `<ups>/USV`, `<ups>/Batterie`), UniFi `Sensor N` | snmp, nut |
| `fan.rpm` | rpm | fan name (`Lüfter N`) | snmp (UniFi) |
| `poe.power`, `poe.budget` | W | | snmp `snmp:poe` (consumption, nominal power of the PSE) |
| `poe.usage` | % | | snmp `snmp:poe` (default threshold warn 85, crit 95) |
| `poe.port` | W | ifName | snmp (ports that deliver PoE; per-port value from the vendor table) |
| `printer.toner` | % | color/supply name | snmp |
| `printer.pages` | "" | | snmp |
| `custom` | from config | CustomOID.Name | snmp |

## Evaluation (core)

1. Collector returns `[]model.Check` with native State.
2. For each metric the matching threshold is chosen: device threshold with instance > device
   threshold by name > global (settings) with instance > global by name > `model.DefaultThresholds()`.
   `Disabled` switches it off. `Below` inverts the comparison (value ≤ warn → warn).
   Proxmox health defaults: `cpu.iowait` 10/25 % (5×), `disk.temp` 60/70 °C (2×), `disk.wearout` 90/98 %,
   `disk.spare` below 20/10 %, `disk.pending` 1/10, `disk.reallocated`, `disk.uncorrectable`, `disk.media_errors`
   warn from 1, `zfs.usage` 80/90 %, `zfs.errors` warn from 1, `zfs.scrub_age` warn from 35 days.
3. Threshold state needs `Count` consecutive bad values (`CheckStatus.Pending`), going back to ok
   is immediate. Native states (service stopped …) are immediate.
4. Effective check state = worst(native, thresholds). `CheckStatus.Reason` e.g.
   `"Festplatte / 91 % ≥ 85 %"`. Device state = worst of all checks that are not unknown (unknown only when every check is unknown, e.g. no data yet; a single check without permission must not colour the device) (`reach` crit when unreachable:
   poll error twice in a row; agent: no report for max(3×interval, 180 s)).
5. Alerts: when an effective check state becomes warn/crit (and the device is enabled, not
   `NoAlerts`, not in maintenance) an alert opens (store) and a notification is sent
   (`notify.Send`, severity warning/critical). Escalation warn→crit updates the alert and notifies
   again. Back to ok: alert closed + "Wieder OK" (severity info). Checks that disappear (guest
   deleted) close their alerts silently. Optional re-notify every N hours (settings). Events
   (store events table) are written for every alert open/close and for system things.
6. Samples are written to the store for every metric at report/poll time.
7. Check rules (`Device.Rules`, `model.CheckRule{match, mode}`, see `model.Device.CheckMode`): `match` is a
   check key or a pattern where `*` matches any text (`snmp:if:*`, `pve:vm:*`, `disk:/boot*`); an exact rule
   wins over patterns, among patterns the longest. `reach` is always `alert`. Modes:
   - `alert` (default): as above.
   - `quiet`: collected, metrics stored, evaluated as usual (thresholds, hysteresis/`Pending`), then a warn/crit
     result is shown as `State` ok with `CheckStatus.Mode` "quiet", `Muted` = the warn/crit state and `Reason`
     kept. Unknown stays unknown (ignored for the device state like every unknown check). Never alerts; it
     counts as its State (ok) in device state, check counts and summary. The threshold state survives a
     restart through `Muted`.
   - `off`: the check is dropped with its metrics before anything is stored (polls, net checks, agent
     reports incl. buffered ones, demo back-fill) and listed in `DeviceStatus.Hidden` `{key, name, group}`
     (sorted by group, name; agents: report checks from the last report, network checks from the last poll).
   `SaveDevice` re-applies the rules to the current status at once (quiet/alert from the stored verdict, off
   checks move to Hidden, Hidden entries no longer off are dropped and come back with the next poll/report)
   and syncs the alerts: an open alert of a check that became quiet or off closes silently (event
   "Alarm beendet" – "Der Check wird nicht mehr alarmiert (nur anzeigen)." / "Der Check wurde
   ausgeblendet."); quiet → alert opens it again at once when the check is still bad.
   Validation (`rules.N.match` / `rules.N.mode`): match required, ≤ 200 characters, no duplicates, no rule that
   only matches `reach`; mode `alert` (default when empty), `quiet` or `off`; at most 200 rules.
8. Discovery (`core/discovery.go`): the first successful collection (poll of proxmox/snmp/nut, fresh agent
   report) records every check key it reports (before rules switch any off; not `reach`, `discovery`, `net:*`)
   in `DeviceStatus.Inventory` (`{key, name, group, first, last, new, missing}`, persisted with the status, left
   out of API responses). Keys reported later are new (`new: true`) unless a rule matches them, the device has
   `auto_accept`, or the selection changed just before (host, proxmox/snmp/nut config, the agent's service
   lists, the central service lists in `settings.agent`): then the next collection is the new baseline – its
   unseen keys are accepted and the keys it misses are dropped (deselected, not vanished). A new check runs in mode `new`
   (like quiet: stored and shown, `muted` holds warn/crit, never alerts) until a rule matches it or it is
   accepted. A key missing from 2 successful collections in a row is vanished (`DeviceStatus.Vanished`
   `{key, name, group, last}`) until it comes back or is removed; a new key that vanishes before a decision is
   forgotten. `Core.Discovery(id, accept, remove)` accepts new keys and removes vanished ones (they count as
   new when they return), re-applies the modes and re-evaluates the `discovery` check at once. A new server
   version (setting `version`) and a new agent version make the next collection the baseline as well (an update
   brings new checks, it does not discover them) (events "Neue
   Dienste übernommen" / "Verschwundene Dienste entfernt"). Inventory ≤ 5000 keys per device.

## Attached agents (agent + API on one device)

`model.Device.ParentID`/`Node` (`internal/core/attach.go`): an agent device can be attached to any non-agent
device (Proxmox, SNMP, NUT, net). The agent's data complements the parent's API/SNMP data; for a Proxmox cluster
an agent belongs to a node (`Node` = PVE node name).

- The attached agent keeps its own id, checks, metrics, thresholds, rules, alerts and detail page
  (`/devices/<childId>`, with a link back to the parent). `ParentID`/`Node` change only through attach/detach and
  agent registration; `SaveDevice` keeps the stored values (a changed `parent_id` pointing to the device itself or
  to an agent is a field error).
- Lists: `/api/overview` and `/api/devices` show only top-level devices; `DeviceRow.attached` counts the attached
  agents. A parent's `state`, `message`, `since`, `last_poll` and check counts include its attached agents (worst
  state; message prefixed with the node/agent name, e.g. `"pve1: Festplatte /: …"`; several problems:
  `"N Probleme: a; b; …"`). Overview counts and the external Summary count each top-level device once. Disabled
  agents and agents in maintenance do not colour the parent. `/api/agents` lists all agent devices (attached ones
  with `parent_name`). `GET /api/devices/{id}` `status` stays the device's own.
- Alerts, events and notifications of an attached agent use the device name `"<parent> / <node or agent name>"`.
- Proxmox: `pve:node:<node>` of the parent gets the fact "Agent" = `"<agent name> · <state word> · v<version>"`
  when an attached agent has that node.
- Deleting a parent detaches its agents (they become standalone again; the event says so); deleting an attached
  agent just deletes it.
- Suggestions (unattached agent): parents whose Proxmox node name/IP, host or IPs match the agent hostname
  (case-insensitive short name) or IPs.
- Registration with `AgentRegister.DeviceID` (from `pk95agent install --device ID [--node NODE]`): the device must
  exist and must not be an agent, a node must be a known node of a Proxmox parent (else German error, HTTP 400).
  The new or re-registered (same AgentID) agent device gets ParentID/Node; Node defaults for Proxmox parents to the
  PVE node whose name equals the short hostname. Re-registering without DeviceID keeps the attachment.

## SNMP profiles

`SNMPConfig.Profile`: `auto` (default), `generic`, `switch`, `unifi`, `ups`, `printer`, `nas`.

- `auto` detects from sysObjectID/sysDescr; `unifi` before the "more than 4 Ethernet ports → switch" rule:
  sysDescr `^(US|USW)[ -]`, `^UBNT US`, `^USW GigaBit`, `^USW-`; sysObjectID `1.3.6.1.4.1.4413` with
  agentInventoryMachineType starting `US`, `UBNT`, `EdgeSwitch` or `EdgePoint` (EdgeSwitch: same MIBs); exactly `1.3.6.1.4.1.41112.1` (US-8, not the APs);
  net-snmp `8072.3.2.10` with sysDescr `^Linux UBNT`. Gen2 switches that report only `Linux <host> … mips` stay
  `generic` (choose `unifi` by hand).
- Switch-like profiles (`switch`, `unifi`) read BRIDGE-MIB base ports, LLDP-MIB neighbors (cached 5 min) and
  POWER-ETHERNET-MIB (RFC 3621) plus the Broadcom vendor PoE table (`1.3.6.1.4.1.4413.1.1.15`, per-port mW) when
  the sysObjectID is in 4413. All of these are optional: missing tables add nothing.
- `unifi` additionally reads the Broadcom FASTPATH tree (`1.3.6.1.4.1.4413.1.1`, fallback `41112.1.1`):
  inventory (facts Modell, Firmware, Seriennummer; ENTITY-MIB as last fallback), CPU (`snmp:cpu` from the
  "60 Secs" value of the CPU string, then the integer OID, then hrProcessorLoad), memory (`snmp:mem`, FASTPATH
  KB × 1024, else UCD-SNMP-MIB), boxServices temperatures, fans and power supplies. Rows that exist only in
  ifXTable (US-8 Vitesse) get rx/tx from their 64-bit counters.
- Facts and values not verified on real UniFi hardware (per-port PoE watts, the integer CPU OID, the fan speed
  format) are read defensively; when absent the port shows "PoE: aktiv" without watts.

## NUT (Network UPS Tools)

Device type `nut` (`model.NUTConfig{port, ups, username, password}`; port default 3493): `internal/collect/nut`
connects to upsd at `Device.Host`, optionally logs in (USERNAME/PASSWORD), runs `LIST UPS` and `LIST VAR <ups>`
(one connection per poll, deadline = poll timeout, max 10 s). Empty `ups` = every UPS the server lists; when
several UPS are polled by one device every UPS metric gets instance `<ups>` (temp `<ups>/USV`, `<ups>/Batterie`).
Errors (unreachable, refused, timeout, login or listing refused, no UPS on the server) make the poll fail
(`reach`); a configured UPS the server does not list, `DATA-STALE`, `DRIVER-NOT-CONNECTED` etc. become an unknown
check.

State from the `ups.status` flags (shown in NUT's order): `OL` ok "Netzbetrieb", `OL CHRG` ok "Netzbetrieb, Akku
lädt", `OB` warn "Läuft auf Batterie", `LB` crit "Akku schwach", `RB` warn "Akku tauschen", `OVER` crit
"Überlast", `BYPASS` warn "Bypass", `OFF` crit "Ausgeschaltet", `FSD` crit "Wird heruntergefahren", `ALARM` warn
(+ `ups.alarm`), `WAIT` unknown; HB, CHRG, DISCHRG, CAL, TRIM, BOOST, TEST, ECO are ok; unknown flags are shown
in the message. Message: status text, then " – Ladung X %, Restlaufzeit N min, Last Y %".

Validation (`nut.*` field keys): host required; `nut.port` 1–65535; `nut.ups` empty or
`[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}` (no `ups@host`); `nut.username` ≤ 64 characters without blanks/line breaks,
required when a password is set; `nut.password` ≤ 128 characters without line breaks, empty = keep the stored
one.

## Store (SQLite, modernc.org/sqlite, WAL)

File `<data dir>/pk95monitoring.db`. Base taken from `~/code/pk95proxbackup/internal/store/store.go`
(settings, objects, users, sessions, events incl. `details`, migrations via `PRAGMA user_version`,
run automatically on start), plus:

- `series(id INTEGER PK, device_id, name, instance, unit, check_key, last_ts, last_value, UNIQUE(device_id,name,instance))`
- `samples(series_id, ts, value, PRIMARY KEY(series_id, ts)) WITHOUT ROWID` – raw, default 2 days
- `rollups(series_id, res, ts, min, max, avg, cnt, PRIMARY KEY(series_id,res,ts)) WITHOUT ROWID` – res 300 (default 35 days) and 3600 (default 400 days)
- `alerts(id PK, device_id, device_name, check_key, check_name, severity, message, started, ended, acked_by, acked_at)`
- `statuses(device_id PK, data TEXT)` – persisted DeviceStatus JSON
- `api_tokens(id PK, name, token_hash UNIQUE, prefix, created, last_used)`
- `agent_tokens(device_id PK, token_hash UNIQUE, created)`

Query picks the source by range: ≤ 48 h raw, ≤ 35 d 5-min rollups, else hourly, and buckets
into the requested step (avg/min/max per bucket).

## REST API (UI ↔ server)

Session cookie `pk95mon_session`; mutating requests need header `X-PK95: 1` (CSRF guard, as in
ProxBackup). Errors: `{"error": "…", "fields": {"name": "…"}}` with 4xx. Times are unix seconds.
Secrets (`proxmox.secret`, `snmp.community`, `snmp.auth_pass`, `snmp.priv_pass`, `nut.password`,
notify passwords/tokens, central token) are never sent back: they come as `""` plus
`"has_secret": true`-style flags (`proxmox.has_secret`, `snmp.has_community`, `snmp.has_auth_pass`,
`snmp.has_priv_pass`, `nut.has_password`); on save an empty secret keeps the stored one.

Public:
- `GET /api/session` → `{setup, version, instance, user?}` (`user` only with a valid session)
- `POST /api/login {username, password}` → `{user}` (rate limited: 10 failures / 15 min per client); `POST /api/logout` → `{ok}`; `POST /api/setup {code, username, password}` → `{user}`

Authenticated:
- `GET /api/overview` → `{counts:{ok,warn,crit,unknown,maintenance,disabled}, devices:[DeviceRow], alerts:[Alert active], events:[Event ×10], groups:[string]}`
- `DeviceRow` = `{id, name, type, host, group, enabled, no_alerts, maintenance_until, state, message, since, last_poll, os, platform, agent_version, checks:{ok,warn,crit,unknown}, cpu, mem, disk, parent_id?, parent_name?, attached}` (cpu/mem/disk: latest % or null; disk = max disk.usage; a parent without own values shows the highest of its agents; `attached` = number of attached agents)
- `GET /api/devices` → `{devices:[DeviceRow], groups:[string]}` (overview and list: top-level devices only, see "Attached agents")
- `POST /api/devices` (Device) → Device; `PUT /api/devices/{id}` → Device; `DELETE /api/devices/{id}` → `{ok}`
  (`rules: [{match, mode}]` round-trips, never `null`)
- `GET /api/devices/{id}` → `{device: Device (redacted), status: DeviceStatus, alerts:[Alert ×20]}` plus, for a
  non-agent device, `attached: [{device, status}]` (sorted by node, name; `[]` when none); for an attached agent
  `parent: {id, name, type}`; for an unattached agent `suggest: [{id, name, type, node}]`
- `POST /api/devices/{id}/attach {agent_id, node}` → `{device}` (the parent; 422 with German field errors on
  `agent_id`/`node`, 404 for an unknown parent); `POST /api/devices/{id}/detach` (id = the attached agent) → `{device}`
  (`status.checks[].mode` "quiet" and `.muted` warn/crit for quiet checks, `status.hidden: [{key, name, group}]`
  for checks switched off by a rule, omitted when empty)
- `POST /api/devices/{id}/poll` → `{status}`
- `POST /api/devices/{id}/maintenance {until, note}` (until 0 = end) → Device
- `POST /api/devices/{id}/discovery {accept:[key], remove:[key]}` → `{accepted, removed, status}` (unknown keys
  ignored; only new keys are accepted, only vanished keys removed). `status.checks[].mode` "new" marks new
  checks, `status.vanished: [{key, name, group, last}]` the vanished ones; Device has `auto_accept`.
- `GET /api/devices/{id}/series` → `{series:[{id, device_id, name, instance, unit, check_key, last_value, last_ts}]}`
- `GET /api/devices/{id}/metrics?series=1,2&range=1h|6h|24h|7d|30d|1y` (default 24h, at most 16 series; step ≥ poll interval) → `{from, to, step, series:[{id, name, instance, unit, points:[[ts, avg, min, max], …]}]}`
- `GET /api/devices/{id}/sparks?range=1h|6h|24h` (default 6h; other ranges 400) → `{from, to, step, capped,
  series:[{id, check_key, name, instance, unit, last, last_ts, values:[number|null, …]}]}`: every series of the device
  (ordered by name, instance) for the check overview of the device page. `values` are bucket averages, oldest first;
  `step` = span / 48, at least the poll interval in whole minutes (so 48 buckets, fewer for slowly polled devices),
  buckets aligned to multiples of `step`, the last one contains `to`, `from` = start of the first bucket; empty
  buckets are `null`; 6 significant digits. `last` = latest value (full precision, `null` without one). At most 600
  series (the most recently updated ones win, `capped: true`; otherwise `capped: false`); one store query for all
  series (600 series × 6 h of 60 s samples: ≈ 0.2 s). An attached agent is queried with its own id.
- UI route `/devices/<id>/checks/<encodeURIComponent(check key)>` = check detail page (charts over time via
  `/series` + `/metrics`). The server answers every path below `/devices/` with `index.html`, even with a dot
  in it (keys like `svc:ssh.service`, `if:eth0.100`).
- `POST /api/probe {host, port}` → `{fingerprint}` (TLS fingerprint for Proxmox)
- `POST /api/test/proxmox {id?, host, proxmox}` → `{ok, version, nodes:[…], guests, message}`
- `POST /api/test/snmp {id?, host, snmp}` → `{ok, sys_name, sys_descr, profile, interfaces:[{name, descr, alias, speed, oper}], message}`
  (test endpoints answer 200 with `{ok:false, message}` when the device cannot be reached)
- `POST /api/test/nut {id?, host, nut}` → `{ok, server, ups:[{name, desc, model, status}], message}` (empty
  `nut.password` with `id` = the stored password; `status` is German text)
- `POST /api/test/netcheck {host, check}` → `Check`
- `GET /api/alerts?state=active|all&before=&limit=&device=` (limit default 50, max 500) → `{alerts, more}`; `POST /api/alerts/{id}/ack` → `{ok}`
- `GET /api/events?severity=&before=&limit=` (limit default 50, max 500) → `{events, more}` (Event as in ProxBackup: `{id, ts, severity, source, title, body, details}`)
- `GET /api/agents[?device=<id>&node=<node>]` → `{enroll_key, server_url, fingerprint, version, auto_update, linux_cmd, windows_cmd, binaries:[{os, arch, file, size, sha256, available}], agents:[DeviceRow]}` (`linux_cmd`/`windows_cmd` empty when the binary is missing; see Agent → one-liners; with `device` the commands include `--device <id> [--node <node>]`, 404 for an unknown device, 400 for an agent; `agents` = all agent devices incl. attached ones)
- `POST /api/agents/key` → `{enroll_key}` (rotate)
- `GET /api/settings` → `{general:{instance, public_url, retention_raw_days, retention_5m_days, retention_1h_days, renotify_hours}, notify: notify.Settings (redacted), thresholds:[Threshold], defaults:[Threshold], central:{enabled, url, has_token, interval}, agent:{auto_update, windows_services:[string], linux_services:[string]}}` (service lists: names or patterns, ≤ 200 entries of ≤ 256 characters, never `null`)
- `PUT /api/settings` (same shape; missing sections unchanged) → same as GET
- `POST /api/settings/test {notify?}` → `{ok}` (unsaved form values; missing secrets from the store); `POST /api/settings/telegram/chats {token?}` → `{chats}`
- `GET /api/tokens` → `{tokens:[{id, name, prefix, created, last_used}]}`; `POST /api/tokens {name}` → `{token, …}` (plain token only once); `DELETE /api/tokens/{id}`
- `POST /api/account/password {old, new}` → `{ok}`
- `GET /api/system` → `{version, instance, started, db_size, devices, series, data_dir, demo}`

Agent endpoints (no session; TLS):
- `GET /agent/install.sh`, `GET /agent/install.ps1` – templates filled with server URL
- `GET /agent/download/{os}/{arch}` – binary; `GET /agent/version` → `{version, binaries:[…]}`
- `POST /agent/v1/register` (AgentRegister) → AgentRegistered (401 on wrong key; 400 with a German message for a bad `device_id`/`node`)
- `POST /agent/v1/report` (`Authorization: Bearer <token>`, AgentReport) → AgentReply

External API (`Authorization: Bearer <api token>`, read-only):
- `GET /api/v1/summary` → Summary
- `GET /api/v1/devices` → `{devices:[DeviceSummary]}`
- `GET /api/v1/devices/{id}` → `{device: DeviceSummary, checks:[CheckStatus], rules:[CheckRule]}`
- `GET /api/v1/alerts?state=active|all&before=&limit=` (default active; `all`: limit default 100, max 1000) → `{alerts}`

Rate limit 60 requests per minute and client (429 with `Retry-After: 60`); 401 with `WWW-Authenticate: Bearer` without or with a wrong token. Tokens start with `pk95_`.

Central push (settings.central): every `interval` minutes `POST <url>` with Summary JSON and
`Authorization: Bearer <token>`; failures become a warning event (once, not every time).

## Agent

- Binary `pk95agent` (linux/amd64, linux/arm64, windows/amd64; CGO-free). Config JSON: Linux
  `/etc/pk95agent/agent.json`, Windows `C:\ProgramData\PK95Agent\agent.json` (`server`, `token`,
  `device_id`, `fingerprint`, `insecure`, `interval`, `services`, `watch_auto_services`,
  `ignore_services`, `central_services`); every command takes `--config PATH`.
- Services (`agent/filter.go` `pickServices`, the same on Windows and Linux): entries of the device's
  `services` without `*` are required names (must exist and run; keyed `svc:<configured spelling>`, crit "Dienst
  nicht gefunden" when missing, never ignored). Entries with `*` and every entry of `central_services` (the
  server's `settings.agent.windows_services` / `linux_services` for the agent's OS, sent with every reply) are
  optional: they pick each matching service (case-insensitive glob on the name, the name without `.service`, or
  the display name/description) that runs or starts automatically (Windows: start type automatic without
  triggers; Linux: unit file enabled), minus `ignore_services` (names or patterns); keyed `svc:<name>` (Linux
  without `.service`). Within the first 10 minutes after boot a picked optional service that does not run yet is
  unknown ("startet nach dem Systemstart noch"). Linux reads `systemctl list-units --type=service --all` and
  `list-unit-files --type=service` only when there is a pattern or central entry.
- Install: `pk95agent install --server URL --key KEY [--fingerprint SHA256] [--insecure] [--device ID [--node NODE]]`
  (root/Administrator) registers (with `--device`: attached to that device, see "Attached agents"), writes the config, copies itself to `/usr/local/bin/pk95agent` or
  `C:\Program Files\PK95Agent\pk95agent.exe`, installs and starts the service (systemd / Windows
  SCM via kardianos/service; re-install replaces an existing service). `uninstall` removes service,
  config and buffer (Linux also the binary). `run` = service mode (also usable in the foreground).
  `once` prints one report as JSON (debug, works without registration). `version`.
- TLS: pin the server certificate by SHA-256 fingerprint of the leaf (hex, colons optional) when
  given; otherwise system CAs (or `--insecure`). `http://` servers are accepted with a warning.
- Every interval collect `[]model.Check` (see check keys), POST report. On failure keep reports in
  a ring buffer (max 1440, `buffer.jsonl` next to the config) and send them oldest first later with
  `buffered: true`.
- Self-update when `AgentReply.Update` is set (setting `agent.auto_update`, default on): download,
  verify SHA-256, replace binary, restart.
- One-liners (`agentdist.Commands`, shown on the Agents page; a missing binary = empty command):
  - Linux: `curl -fsSL [-k --pinnedpubkey 'sha256//<SPKI>'] '<url>/agent/install.sh' | $(command -v sudo) bash -s -- --key '<key>' [--fingerprint <hex>] --sha256-amd64 <hex> --sha256-arm64 <hex>`
    (`-k`/pin/fingerprint only for a self-signed certificate).
  - Windows: `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "…"` (works pasted into
    cmd.exe or PowerShell, as Administrator): TLS 1.2, for a self-signed certificate a validation
    callback that accepts only the certificate with the fingerprint, then `install.ps1` with
    `-Key`, `-Fingerprint`, `-Sha256`.
- Install scripts (`GET /agent/install.sh|.ps1`, templates in `internal/agentdist`): download the
  right binary from `/agent/download/{os}/{arch}`, verify the SHA-256 passed in the one-liner,
  place it and run `pk95agent install …`. They accept `--device`/`--node` (`-Device`/`-Node`) and pass them on;
  the one-liners for a device (`agentdist.Params.DeviceID`/`Node`) add them.

## Demo mode

`PK95MON_DEMO=1`: `internal/fake.Setup(c)` adds (if not present) a Proxmox cluster backed by
`fakepve` (3 nodes, 13 guests incl. a template and a stopped VM that should run, storages, ZFS,
one failing backup), SNMP devices backed by `fakesnmp` (switch with 24 ports + 2 uplinks, 5 without link and 1 disabled, UPS, printer,
Synology NAS; the switch has "Alarm bei Link-Ausfall" for its named ports Gi1/0/1–Gi1/0/6, the free ports at
the end are down and stay ok), a UniFi USW-Pro-24-PoE `demo-unifi` (10.0.0.3, group Netzwerk, profile auto →
unifi; PoE on 12 ports, LLDP neighbors, fans, temperatures; "Alarm bei Link-Ausfall" on 0/13–0/15, 0/24–0/26),
a UniFi UPS 2U `demo-nut` on a `fakenut` server (group Infrastruktur; host/port updated on every start),
5 simulated agents (2 Debian, 2 Windows, plus `demo-pve1` attached to the cluster as node pve1; one with high disk
usage, one with a stopped service; rules: `updates` quiet on db01, `disk:/boot*` off on web01) reporting through
`core.IngestReport` every 30 s, and a net device "Webseite" with HTTP, TLS, TCP and ping checks against local
httptest servers. Existing demo databases get new demo devices (keep/import logic keeps user changes). Values
move realistically (sine + noise). Every demo device without history (all on the first start, new ones on later
starts) is back-filled for 7 days so charts show data. `fake.DiscoveryAfter` (3 min) after the start the cluster
gets a new VM 108 "build01" and loses CT 203 "proxy", so the discovery sections have something to show.

## Web UI notes

- Discovery on the device page (list view): sections "Neu gefunden" (new checks, "Alle übernehmen", checkboxes
  with the bulk bar – Überwachen accepts, Nur anzeigen / Ausblenden set rules and accept) and "Verschwunden"
  ("Entfernen" / "Alle entfernen") come first. The mode menu of a new check has no current mode; a pick also
  accepts it. The device form has "Neue Dienste automatisch überwachen" (`auto_accept`).
- Device page, tab "Übersicht", view "Liste" (default; the choice is kept in localStorage `pk95mon-ckview`):
  one table row per check (checkbox, state, name, one-line summary built from metrics/facts/message, 6 h
  sparkline, since, mode button) in sections by mode – "Überwachte Dienste", "Nur Anzeige", each attached
  agent, "Ausgeblendet" – with group headings. A search field and "Auffällig" filter rows client-side; the
  checkboxes (row, group, section) feed a sticky bar that sets Überwachen / Nur anzeigen / Ausblenden for all
  checked checks (pattern rules where a whole key family is checked, exact rules otherwise).
- Device list `/devices`: one compact row per device – state, name (+ attached agents) with address, type and
  group below and the first problem in one line (full text as tooltip), check counts with the last contact,
  CPU/RAM/disk bars, "Jetzt prüfen"; the whole row opens the device (edit and delete are on the device page).
  Phones get a card per device (name and state, checks, three small bars).
- Settings page `/settings`: one tab per area (`STABS`), the tab in the URL hash – Allgemein (instance, data
  retention, re-notify; system info), `#notify` Benachrichtigungen, `#thresholds` Schwellwerte, `#agents` Agents
  (auto-update, central service lists), `#api` Schnittstellen (API tokens, central push), `#account` Konto
  (password). All panes are rendered at once and toggled, so every form keeps its own save button.
- Device page, tab "Übersicht", view "Kacheln": every check as a card (name, primary value, second line, usage bar, 6 h
  sparkline from `GET /api/devices/{id}/sparks`, reason), grouped as before; attached agents get their own
  card section (sparks requested with the agent's id). Proxmox nodes, guests and storage, switch ports and
  toner stay tables/lists with a sparkline column. Sparks are cached per device for about a minute (one
  request per device and attached agent per refresh). Tab "Alle Diagramme" = the former chart tab.
- Check page, "Was bedeutet das?" (`EXPLAIN` per metric, `EXPLAIN_CHECKS` for states without a metric): for a
  warn/crit check every metric over its effective threshold gets what it measures, why it matters and what to do
  (commands with the disk, pool or service filled in); without such a metric the matching check explanation
  (service stopped, SMART FAILED, pool degraded, …). The other metrics' explanations are folded below.
  Reasons leave out a "node/device" instance the check name already holds ("Platte sdc (pve3)").
- Check page `/devices/<id>/checks/<key>`: every card and table row (incl. each switch port) opens it. Header
  with back link (attached agent: parent `#agent-<id>`), mode menu and "Jetzt prüfen"; current value; one chart
  per metric of the check (pairs like rx/tx and used/total share one) for 1 h … 1 y via `/series` + `/metrics`;
  values and facts; effective thresholds; alert history (`/api/alerts?device=<id>&state=all`, filtered by key).
- Device types in the UI: Proxmox VE, Agent, SNMP, Netzwerk, "USV (NUT)".
