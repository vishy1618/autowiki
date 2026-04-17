.PHONY: build dev test clean

# CGO flags for RocksDB. Homebrew on Apple Silicon installs headers under
# /opt/homebrew; override via environment if your setup differs.
CGO_CFLAGS ?= -I/opt/homebrew/include

build:
	cd web && npm run build
	rm -rf public && mkdir -p public
	cp -r web/build/client/. public/
	CGO_CFLAGS="$(CGO_CFLAGS)" go build -o bin/autowiki ./cmd/server

GOTESTSUM := $(shell go env GOPATH)/bin/gotestsum

test:
	CGO_CFLAGS="$(CGO_CFLAGS)" $(GOTESTSUM) --format short-verbose -- -count=1 -cover ./...

dev:
	@echo "Run these in separate terminals:"
	@echo "  make dev-ui"
	@echo "  make dev-server"

dev-ui:
	cd web && npm run dev

dev-server:
	go run ./cmd/server --dev

clean:
	rm -rf bin/ public/ web/build/
