#!/usr/bin/env bash
# Reconciles the recorded active slot against systemd only.
#
# nginx is deliberately out of scope here: locating its config needs DOMAIN and
# container settings this script does not take, and a second implementation
# would be free to disagree with the one that matters. deploy-blue-green.sh
# reconciles against nginx immediately after calling this, and nginx wins there
# because it is the source that decides where traffic actually goes.
#
# So a green result from this script means "the record matches systemd", not
# "the deployment is consistent". Do not use it alone to conclude the latter.
set -euo pipefail

SERVICE_NAME="${SERVICE_NAME:-clirelay2}"
BASE_DIR="${BASE_DIR:-/opt/clirelay2}"
PORT_A="${PORT_A:-8318}"
PORT_B="${PORT_B:-8319}"
ACTIVE_PORT_FILE="${ACTIVE_PORT_FILE:-${BASE_DIR}/.active-port}"

trim_port() {
	printf '%s' "${1:-}" | tr -d '[:space:]'
}

service_is_active() {
	systemctl is-active --quiet "${SERVICE_NAME}-$1"
}

write_active_port() {
	printf '%s\n' "$1" > "$ACTIVE_PORT_FILE"
}

recorded_port="$(trim_port "$(cat "$ACTIVE_PORT_FILE" 2>/dev/null || true)")"

port_a_active=0
port_b_active=0
if service_is_active "$PORT_A"; then
	port_a_active=1
fi
if service_is_active "$PORT_B"; then
	port_b_active=1
fi

case "$recorded_port" in
	"$PORT_A")
		if [ "$port_a_active" -eq 1 ]; then
			printf '%s\n' "$PORT_A"
			exit 0
		fi
		;;
	"$PORT_B")
		if [ "$port_b_active" -eq 1 ]; then
			printf '%s\n' "$PORT_B"
			exit 0
		fi
		;;
esac

if [ "$port_a_active" -eq 1 ] && [ "$port_b_active" -eq 0 ]; then
	write_active_port "$PORT_A"
	echo "reconciled stale active slot from ${recorded_port:-unknown} to ${PORT_A}" >&2
	printf '%s\n' "$PORT_A"
	exit 0
fi

if [ "$port_b_active" -eq 1 ] && [ "$port_a_active" -eq 0 ]; then
	write_active_port "$PORT_B"
	echo "reconciled stale active slot from ${recorded_port:-unknown} to ${PORT_B}" >&2
	printf '%s\n' "$PORT_B"
	exit 0
fi

if [ "$port_a_active" -eq 1 ] && [ "$port_b_active" -eq 1 ]; then
	case "$recorded_port" in
		"$PORT_A"|"$PORT_B")
			printf '%s\n' "$recorded_port"
			exit 0
			;;
	esac
	echo "both deploy slots are active but ${ACTIVE_PORT_FILE} is missing or invalid; refusing to guess active slot" >&2
	exit 1
fi

echo "no active deploy slot is running; recorded active slot ${recorded_port:-unknown} is stale" >&2
exit 1
