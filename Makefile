.PHONY: fmt vet test check build image smoke bench run

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

test:
	go test ./... -race

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test ./... -race

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
