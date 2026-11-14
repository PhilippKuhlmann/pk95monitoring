#!/bin/sh
# PK95 Monitoring – update helper on the Docker host.
#
# "Einstellungen → Aktualisierung → Installieren" in the web UI writes update/request.json into
# the data volume of the container. A systemd path unit on the host notices the file and runs
# this script, which updates the container – with ./update.sh <tag> when the compose directory
# has one, otherwise with "docker compose pull" and "docker compose up -d" – and reports back in
# update/status.json and update/update.log. The container itself needs no access to Docker.
#
#   sudo sh updater.sh install [CONTAINER]     set up the units (default container: pk95monitoring)
#   sudo sh updater.sh uninstall [CONTAINER]   remove them
#   updater.sh run CONTAINER DATA DIR SERVICE  called by systemd
set -eu

LIB=/usr/local/lib/pk95monitoring
UNITS=/etc/systemd/system

die() { echo "updater: $*" >&2; exit 1; }

inspect() { docker inspect -f "$2" "$1" 2>/dev/null || true; }

install_units() {
	container="${1:-pk95monitoring}"
	[ "$(id -u)" = 0 ] || die "bitte mit sudo bzw. als root ausführen"
	command -v docker >/dev/null || die "docker nicht gefunden"
	command -v systemctl >/dev/null || die "systemd nicht gefunden"
	data=$(inspect "$container" '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}')
	[ -n "$data" ] || die "Container $container nicht gefunden oder ohne /data – läuft er?"
	dir=$(inspect "$container" '{{index .Config.Labels "com.docker.compose.project.working_dir"}}')
	service=$(inspect "$container" '{{index .Config.Labels "com.docker.compose.service"}}')
	[ -n "$dir" ] && [ -n "$service" ] || die "$container wurde nicht mit docker compose gestartet"
	mode=compose
	[ -x "$dir/update.sh" ] && mode=update.sh

	mkdir -p "$LIB" "$data/update"
	cp "$0" "$LIB/updater.sh"
	chmod 0755 "$LIB/updater.sh"
	# the container (uid 10001) writes the request
	chown "$(stat -c %u:%g "$data")" "$data/update"
	chmod 0755 "$data/update"

	unit="pk95mon-update-$container"
	cat >"$UNITS/$unit.path" <<EOF
[Unit]
Description=PK95 Monitoring: update request of $container

[Path]
PathExists=$data/update/request.json
Unit=$unit.service

[Install]
WantedBy=multi-user.target
EOF
	cat >"$UNITS/$unit.service" <<EOF
[Unit]
Description=PK95 Monitoring: update $container

[Service]
Type=oneshot
ExecStart=$LIB/updater.sh run "$container" "$data" "$dir" "$service"
TimeoutStartSec=30min
EOF
	printf '{"version":1,"mode":"%s","installed":%s}\n' "$mode" "$(date +%s)" >"$data/update/helper.json"
	chmod 0644 "$data/update/helper.json"
	systemctl daemon-reload
	systemctl enable --now "$unit.path" >/dev/null
	echo "Eingerichtet für $container ($mode in $dir). Updates lassen sich jetzt im Monitoring unter Einstellungen → Aktualisierung installieren."
}

uninstall_units() {
	container="${1:-pk95monitoring}"
	unit="pk95mon-update-$container"
	systemctl disable --now "$unit.path" >/dev/null 2>&1 || true
	rm -f "$UNITS/$unit.path" "$UNITS/$unit.service"
	systemctl daemon-reload
	data=$(inspect "$container" '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}')
	if [ -n "$data" ]; then rm -f "$data/update/helper.json"; fi
	echo "Entfernt für $container."
}

status() { # state tag started [finished code]
	printf '{"state":"%s","tag":"%s","started":%s,"finished":%s,"code":%s}\n' "$1" "$2" "$3" "${4:-0}" "${5:-0}" >"$DATA/update/status.json.tmp"
	chmod 0644 "$DATA/update/status.json.tmp"
	mv "$DATA/update/status.json.tmp" "$DATA/update/status.json"
}

run_update() {
	[ $# -eq 4 ] || die "Aufruf: updater.sh run CONTAINER DATA DIR SERVICE"
	DATA="$2" dir="$3" service="$4"
	req="$DATA/update/request.json"
	[ -f "$req" ] || exit 0
	tag=$(sed -n 's/.*"tag"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$req" | head -n 1)
	rm -f "$req"
	started=$(date +%s)
	if ! printf '%s' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
		status failed "invalid" "$started" "$started" 2
		exit 1
	fi
	status running "$tag" "$started"
	log="$DATA/update/update.log"
	cd "$dir"
	code=0
	if [ -x ./update.sh ]; then
		./update.sh "$tag" >"$log" 2>&1 || code=$?
	else
		{ docker compose pull "$service" && docker compose up -d "$service"; } >"$log" 2>&1 || code=$?
	fi
	chmod 0644 "$log"
	if [ "$code" = 0 ]; then
		status done "$tag" "$started" "$(date +%s)" 0
	else
		status failed "$tag" "$started" "$(date +%s)" "$code"
	fi
}

case "${1:-}" in
install) shift; install_units "$@" ;;
uninstall) shift; uninstall_units "$@" ;;
run) shift; run_update "$@" ;;
*) die "Aufruf: updater.sh install|uninstall [CONTAINER]" ;;
esac
