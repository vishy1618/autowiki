.PHONY: build dev clean

build:
	cd web && npm run build
	rm -rf public && mkdir -p public
	cp -r web/build/client/. public/
	go build -o bin/autowiki ./cmd/server

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
