BINARY  := bin/redlite
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -X github.com/BViganotti/redlite/internal/version.Commit=$(COMMIT)

.PHONY: all build test race vet fmt fmtcheck cover run docker differential cpp-build cpp-test cpp inventory parity clean

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

cpp-build:
	cmake -S cpp -B cpp/build -G Ninja -DCMAKE_BUILD_TYPE=Release
	cmake --build cpp/build -j

cpp-test: cpp-build
	./cpp/build/redlite_tests

differential: build
	./scripts/differential.sh

inventory:
	go run ./cmd/redis-inventory -addr "$${REDIS_ADDR:-127.0.0.1:6379}" -out internal/server/testdata/redis-commands.json

parity:
	go test ./internal/server -run TestRedisCommandInventory -v

clean:
	rm -rf bin coverage.out cpp/build
