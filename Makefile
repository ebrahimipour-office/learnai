# Needs a POSIX shell (Linux, macOS, or Git Bash on Windows).
VERSION ?= 1.0.0
DATE    := $(shell date -u +%Y-%m-%d)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(DATE)
export CGO_ENABLED = 0
# Not ./...: the gitignored topics/ data dir can hold learners' .go answers.
PKGS := ./cmd/... ./internal/... ./skills/...

.PHONY: test vuln release clean

test:
	test -z "$$(gofmt -l cmd internal skills)"
	go vet $(PKGS)
	go test $(PKGS)

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 $(PKGS)

release: test
	rm -rf dist
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub.exe ./cmd/hub
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub ./cmd/hub
	cp INSTALL.md dist/

clean:
	rm -rf dist
