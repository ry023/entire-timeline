.PHONY: build test vet fmt

build:
	go build -o bin/entire-timeline ./cmd/entire-timeline

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal
