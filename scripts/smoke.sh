#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
port=${LIMEN_SMOKE_PORT:-$((20000 + ($$ % 20000)))}
pid=

cleanup() {
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

go build -trimpath -o "$tmp/limen" "$root/cmd/limen"
LIMEN_ADDR="127.0.0.1:$port" \
LIMEN_API_KEY=smoke-limen-key \
OPENAI_API_KEY=smoke-openai-key \
ANTHROPIC_API_KEY=smoke-anthropic-key \
LIMEN_MODELS_FILE="$root/configs/models.example.json" \
"$tmp/limen" >"$tmp/server.log" 2>&1 &
pid=$!

i=0
while [ "$i" -lt 50 ]; do
	if curl -fsS "http://127.0.0.1:$port/livez" >/dev/null 2>&1; then
		break
	fi
	i=$((i + 1))
	sleep 0.1
done
if [ "$i" -eq 50 ]; then
	cat "$tmp/server.log"
	exit 1
fi

curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null

status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/v1/models")
[ "$status" = 401 ]
models=$(curl -fsS -H 'Authorization: Bearer smoke-limen-key' "http://127.0.0.1:$port/v1/models")
case "$models" in
	*'"object":"list"'*) ;;
	*)
		echo "models response is not OpenAI-compatible" >&2
		exit 1
		;;
esac

body="$tmp/chat-error.json"
status=$(curl -sS -o "$body" -w '%{http_code}' \
	-H 'Authorization: Bearer smoke-limen-key' \
	-H 'Content-Type: application/json' \
	-d '{"model":"smoke-unknown-model","messages":[{"role":"user","content":"hello"}]}' \
	"http://127.0.0.1:$port/v1/chat/completions")
[ "$status" = 400 ]
grep -q '"code":"unsupported_model"' "$body"
if grep -q 'smoke-unknown-model' "$body"; then
	echo "chat error echoed the requested model" >&2
	exit 1
fi
