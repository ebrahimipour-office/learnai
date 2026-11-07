# Needs a POSIX shell (Linux, macOS, or Git Bash on Windows).
VERSION ?= 1.0.0
DATE    := $(shell date -u +%Y-%m-%d)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(DATE)
export CGO_ENABLED = 0

.PHONY: test vuln release clean

test:
	go vet ./...
	go test ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

release: test
	rm -rf dist
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub.exe ./cmd/hub
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub ./cmd/hub
	cp INSTALL.md dist/

clean:
	rm -rf dist
