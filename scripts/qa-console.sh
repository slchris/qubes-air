#!/bin/bash
# qa-console.sh — run ON the console qube (qubesair-console) to drive its own API.
#
# It reads the API token from the deployment's secrets file so the token never
# has to be pasted into a command line (where `ps` would show it). Subcommands
# map to the console API; the real-machine provider smoke uses them in order.
#
#   qa-console.sh list
#   qa-console.sh create <zone-id> <name> [node]
#   qa-console.sh job <job-id>            # poll until the job is terminal
#   qa-console.sh qube <qube-id>
#   qa-console.sh suspend <qube-id>
#   qa-console.sh resume <qube-id>
#   qa-console.sh release <qube-id>
#   qa-console.sh purge <qube-id> <name>
set -euo pipefail

SECRETS="${QUBESAIR_SECRETS:-/rw/config/qubesair/secrets.env}"
API="${QUBESAIR_API:-http://127.0.0.1:8080/api/v1}"
TOKEN="$(grep -m1 '^QUBES_AIR_API_TOKEN=' "$SECRETS" | cut -d= -f2-)"
[ -n "$TOKEN" ] || { echo "no QUBES_AIR_API_TOKEN in $SECRETS" >&2; exit 1; }

api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$@"; }

# poll_job waits for a job to reach a terminal state and prints its last form.
poll_job() {
  local id="$1" i state
  for i in $(seq 1 240); do
    state="$(api "$API/jobs/$id" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state",""))')"
    case "$state" in
      succeeded|failed|unknown) api "$API/jobs/$id"; echo; return 0 ;;
    esac
    sleep 5
  done
  echo "job $id did not finish in 20m (last state=$state)" >&2
  return 1
}

cmd="${1:?usage: qa-console.sh <list|create|job|qube|suspend|resume|release|purge>}"
shift || true

case "$cmd" in
  list)
    echo "== zones =="; api "$API/zones"; echo
    echo "== qubes =="; api "$API/qubes?page_size=100"; echo
    ;;
  create)
    zone="${1:?zone id}"; name="${2:?name}"; node="${3:-}"
    spec="{\"vcpu\":1,\"memory\":512,\"disk\":20,\"data_disk_gb\":2"
    [ -n "$node" ] && spec="$spec,\"node\":\"$node\""
    spec="$spec}"
    body="{\"name\":\"$name\",\"zone_id\":\"$zone\",\"type\":\"dev\",\"spec\":$spec}"
    echo "POST /qubes $body"
    api -X POST "$API/qubes" -H 'Content-Type: application/json' -d "$body"; echo
    ;;
  job) poll_job "${1:?job id}" ;;
  qube) api "$API/qubes/${1:?qube id}"; echo ;;
  wait)
    id="${1:?qube id}"; want="${2:-healthy}"
    health=""
    for i in $(seq 1 60); do
      health="$(api "$API/qubes/$id" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("agent_health",""))')"
      if [ "$health" = "$want" ]; then echo "agent_health=$health after $((i*5))s"; exit 0; fi
      sleep 5
    done
    echo "agent_health never reached $want (last=$health)" >&2; exit 1
    ;;
  suspend) api -X POST "$API/qubes/${1:?qube id}/stop"; echo ;;
  resume) api -X POST "$API/qubes/${1:?qube id}/start"; echo ;;
  release) api -X DELETE "$API/qubes/${1:?qube id}"; echo ;;
  purge)
    id="${1:?qube id}"; name="${2:?name}"
    api -X POST "$API/qubes/$id/purge" -H 'Content-Type: application/json' \
      -d "{\"confirm\":\"$name\"}"; echo
    ;;
  *)
    echo "unknown subcommand: $cmd" >&2; exit 2 ;;
esac
