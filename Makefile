BIN := onvif-cam-sim
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w

.PHONY: build test release clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/$(BIN)

test:
	go vet ./... && go test ./...

# Static binaries for every supported platform. ffmpeg is a runtime requirement on each.
release:
	@mkdir -p dist
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BIN)_$(VERSION)_darwin_arm64 ./cmd/$(BIN)
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BIN)_$(VERSION)_darwin_amd64 ./cmd/$(BIN)
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BIN)_$(VERSION)_linux_amd64 ./cmd/$(BIN)
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BIN)_$(VERSION)_linux_arm64 ./cmd/$(BIN)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BIN)_$(VERSION)_windows_amd64.exe ./cmd/$(BIN)
	@ls -la dist

clean:
	rm -rf bin dist
