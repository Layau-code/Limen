#!/bin/sh
set -eu

run_tests() {
  go test ./internal/store -run '^TestPostgresIntegration' -count=1 -race -v
}

if [ -n "${LIMEN_TEST_DATABASE_URL:-}" ] || [ -n "${LIMEN_TEST_DATABASE_ADMIN_URL:-}" ] || [ -n "${LIMEN_TEST_DATABASE_ROLE:-}" ]; then
  if [ -z "${LIMEN_TEST_DATABASE_URL:-}" ] || [ -z "${LIMEN_TEST_DATABASE_ADMIN_URL:-}" ] || [ -z "${LIMEN_TEST_DATABASE_ROLE:-}" ]; then
    echo "LIMEN_TEST_DATABASE_URL、LIMEN_TEST_DATABASE_ADMIN_URL 和 LIMEN_TEST_DATABASE_ROLE 必须同时设置" >&2
    exit 1
  fi
  run_tests
  exit 0
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "运行 PostgreSQL 集成测试需要 Docker，或显式提供测试数据库环境变量" >&2
  exit 1
fi

container_name="limen-postgres-integration-$$"
admin_password="limen_admin_password"
app_password="limen_app_password"

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d \
  --name "$container_name" \
  -e POSTGRES_USER=limen_admin \
  -e POSTGRES_PASSWORD="$admin_password" \
  -e POSTGRES_DB=limen_test \
  -p 127.0.0.1::5432 \
  postgres:17-alpine >/dev/null

attempt=0
until docker exec "$container_name" pg_isready -U limen_admin -d limen_test >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    docker logs "$container_name" >&2
    exit 1
  fi
  sleep 1
done

docker exec "$container_name" psql -U limen_admin -d limen_test -v ON_ERROR_STOP=1 \
  -c "CREATE ROLE limen_app LOGIN PASSWORD '$app_password'" >/dev/null

mapped_port="$(docker port "$container_name" 5432/tcp | awk -F: 'NR==1 {print $NF}')"
export LIMEN_TEST_DATABASE_ADMIN_URL="postgres://limen_admin:${admin_password}@127.0.0.1:${mapped_port}/limen_test?sslmode=disable"
export LIMEN_TEST_DATABASE_URL="postgres://limen_app:${app_password}@127.0.0.1:${mapped_port}/limen_test?sslmode=disable"
export LIMEN_TEST_DATABASE_ROLE="limen_app"

run_tests
