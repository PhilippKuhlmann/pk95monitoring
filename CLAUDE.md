# PK95 Monitoring

Monitoring server (one Docker container per customer): Proxmox VE over its API, Linux/Windows
hosts via pk95agent (push over HTTPS, optionally attached to an API/SNMP device), SNMP devices
(incl. UniFi switches), UPS over NUT and network checks. Single Go binary,
SQLite, embedded vanilla-JS SPA in the PK95 Core design (same as `~/code/pk95proxbackup`).
Architecture, keys, metrics and the REST contract: `DESIGN.md`. Shared types: `internal/model`.

- Run tests: `go test -race ./...` (Proxmox VE, SNMP, NUT and Veeam are simulated by `internal/fake/fakepve`, `internal/fake/fakesnmp`, `internal/fake/fakenut`, `internal/fake/fakeveeam`)
- Format: `gofmt -w .`; check JS: `node --check web/app.js`; all of CI's static checks: `make check`
- Demo UI: `.claude/launch.json` → `demo`, or
  `PK95MON_DEMO=1 PK95MON_NO_TLS=1 PK95MON_LISTEN=127.0.0.1:18096 PK95MON_DATA_DIR=data/demo go run .`
- Agent cross-build: `GOOS=windows GOARCH=amd64 go build ./cmd/pk95agent` (must stay CGO-free)
- Code is always English: identifiers, comments, file names, URL routes (`/devices`, `/alerts`,
  `/events`, `/agents`, `/settings`), JSON keys, log messages, commits. Only texts shown to end
  users (UI, notifications, check names/messages, validation messages) are German.
- Design: copy of PK95 ProxBackup / PK95 Core (`web/style.css`, Space Grotesk, cerulean palette,
  radii 2–4 px). Reuse ProxBackup's UI helpers and patterns; keep it in sync with ProxBackup.
- Mutating API requests need the header `X-PK95: 1` (CSRF guard); `web/app.js` sets it in `api()`.
- New Proxmox API calls go into `internal/collect/proxmox` and must also be simulated in `internal/fake/fakepve`.
- Docker/packaging: `Dockerfile` needs BuildKit; keep `ping_group_range` at `"0 65535"` (unprivileged LXC);
  keep env defaults in `main.go`, `Dockerfile` and the README/DESIGN tables in sync.
