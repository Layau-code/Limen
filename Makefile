.PHONY: fmt vet test generate-check check integration build image smoke bench run

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

test:
	go test ./... -race

generate-check:
	go generate ./internal/decision
	git diff --exit-code -- internal/decision/testdata/fixtures.json

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	$(MAKE) generate-check
	go test ./... -race

integration:
	./scripts/postgres-integration.sh

build:
	mkdir -p bin
	go build -trimpath -o bin/limen ./cmd/limen

image:
	docker build -t limen:dev .

smoke:
	./scripts/smoke.sh

bench:
	go test ./internal/gateway -run '^$$' -bench 'BenchmarkRouter' -benchmem

run:
	go run ./cmd/limen
