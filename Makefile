.PHONY: fmt vet test check run

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

run:
	go run ./cmd/limen
