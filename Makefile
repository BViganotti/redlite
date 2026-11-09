BINARY  := bin/redlite
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -X github.com/BViganotti/redlite/internal/version.Commit=$(COMMIT)

.PHONY: all build test race vet fmt fmtcheck cover run docker differential clean

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/redlite

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

fmtcheck:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

run: build
	./$(BINARY)

docker:
	docker build -t redlite:dev .

differential: build
	./scripts/differential.sh

clean:
	rm -rf bin coverage.out
