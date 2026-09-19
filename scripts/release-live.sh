#!/bin/sh
set -eu

# release-live.sh 只在显式开关开启时调用真实 Provider，避免 CI 或本地检查误产生费用。
if [ "${LIMEN_LIVE_TEST:-}" != "1" ]; then
	echo "真实 Provider 联调已跳过：请设置 LIMEN_LIVE_TEST=1" >&2
	exit 1
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
port=${LIMEN_LIVE_PORT:-$((24000 + ($$ % 10000)))}
pid=

cleanup() {
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

if [ -z "${OPENAI_API_KEY:-}" ] || [ -z "${ANTHROPIC_API_KEY:-}" ]; then
	echo "真实 Provider 联调需要 OPENAI_API_KEY 和 ANTHROPIC_API_KEY" >&2
	exit 1
fi
if [ -z "${LIMEN_LIVE_OPENAI_MODEL:-}" ] || [ -z "${LIMEN_LIVE_ANTHROPIC_MODEL:-}" ]; then
	echo "真实 Provider 联调需要 LIMEN_LIVE_OPENAI_MODEL 和 LIMEN_LIVE_ANTHROPIC_MODEL" >&2
	exit 1
fi

# 模型名只允许 Provider 常见的安全字符，避免写入临时 JSON 时产生歧义。
validate_model_name() {
	case "$1" in
		*[!A-Za-z0-9._:/-]*|"")
			echo "联调模型名包含不支持的字符" >&2
			exit 1
			;;
	esac
}
validate_model_name "$LIMEN_LIVE_OPENAI_MODEL"
validate_model_name "$LIMEN_LIVE_ANTHROPIC_MODEL"

models_file="$tmp/models.json"
cat >"$models_file" <<EOF
{
  "models": [
    {
      "id": "live-openai",
      "targets": [{"id":"openai-live","provider":"openai","upstream_model":"$LIMEN_LIVE_OPENAI_MODEL","capabilities":["text"],"supports_streaming":true,"quality_tier":1,"context_window":4096,"data_classes":["public"]}]
    },
    {
      "id": "live-anthropic",
      "targets": [{"id":"anthropic-live","provider":"anthropic","upstream_model":"$LIMEN_LIVE_ANTHROPIC_MODEL","capabilities":["text"],"supports_streaming":true,"quality_tier":1,"context_window":4096,"data_classes":["public"]}]
    }
  ]
}
EOF

LIMEN_ADDR="127.0.0.1:$port" \
LIMEN_API_KEY="live-gate-local-key" \
LIMEN_MODELS_FILE="$models_file" \
LIMEN_REQUEST_TIMEOUT="${LIMEN_LIVE_REQUEST_TIMEOUT:-30s}" \
"$root/bin/limen" >"$tmp/server.log" 2>&1 &
pid=$!

i=0
while [ "$i" -lt 100 ]; do
	if curl -fsS --max-time 1 "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; then
		break
	fi
	i=$((i + 1))
	sleep 0.1
done
if [ "$i" -eq 100 ]; then
	echo "真实联调服务未就绪" >&2
	exit 1
fi

base_url="http://127.0.0.1:$port"
models_body="$tmp/models-response.json"
models_status=$(curl -sS --max-time 5 -o "$models_body" -w '%{http_code}' \
	-H 'Authorization: Bearer live-gate-local-key' "$base_url/v1/models")
if [ "$models_status" != "200" ]; then
	echo "模型列表联调失败" >&2
	exit 1
fi
if ! grep -q 'live-openai' "$models_body" || ! grep -q 'live-anthropic' "$models_body"; then
	echo "模型列表未返回联调模型" >&2
	exit 1
fi
echo "模型列表通过"

run_chat_check() {
	label=$1
	model=$2
	stream=$3
	body="$tmp/$label.json"
	if [ "$stream" = "true" ]; then
		status=$(curl -sS -N --max-time 60 -o "$body" -w '%{http_code}' \
			-H 'Authorization: Bearer live-gate-local-key' \
			-H 'Content-Type: application/json' \
			-H 'Accept: text/event-stream' \
			-d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with one short word.\"}],\"max_tokens\":8,\"stream\":true}" \
			"$base_url/v1/chat/completions")
		if [ "$status" != "200" ] || ! grep -q 'chat.completion.chunk' "$body" || ! grep -q 'data: \[DONE\]' "$body"; then
			echo "$label 流式联调失败" >&2
			exit 1
		fi
	else
		status=$(curl -sS --max-time 60 -o "$body" -w '%{http_code}' \
			-H 'Authorization: Bearer live-gate-local-key' \
			-H 'Content-Type: application/json' \
			-d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with one short word.\"}],\"max_tokens\":8}" \
			"$base_url/v1/chat/completions")
		if [ "$status" != "200" ] || ! grep -q 'chat.completion' "$body"; then
			echo "$label 普通响应联调失败" >&2
			exit 1
		fi
	fi
	echo "$label 通过"
}

run_chat_check "openai-normal" "live-openai" false
run_chat_check "openai-stream" "live-openai" true
run_chat_check "anthropic-normal" "live-anthropic" false
run_chat_check "anthropic-stream" "live-anthropic" true

echo "真实 Provider 联调通过"
