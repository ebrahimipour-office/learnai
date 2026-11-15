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
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "ERROR: working tree is dirty; commit or stash changes before building release" >&2; \
		exit 1; \
	fi
	rm -rf dist
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub.exe ./cmd/hub
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hub ./cmd/hub
	cp INSTALL.md dist/
	@if go version -m dist/hub | grep -q "vcs.modified=true"; then \
		echo "ERROR: dist/hub built with dirty VCS state" >&2; \
		exit 1; \
	fi
	@if go version -m dist/hub.exe | grep -q "vcs.modified=true"; then \
		echo "ERROR: dist/hub.exe built with dirty VCS state" >&2; \
		exit 1; \
	fi
	@REV=$$(git rev-parse HEAD); \
	if ! go version -m dist/hub | grep -q "vcs.revision=$$REV"; then \
		echo "ERROR: dist/hub revision does not match HEAD ($$REV)" >&2; \
		exit 1; \
	fi
	@if ! go version -m dist/hub.exe | grep -q "vcs.revision=$$REV"; then \
		echo "ERROR: dist/hub.exe revision does not match HEAD ($$REV)" >&2; \
		exit 1; \
	fi
	@diff -u INSTALL.md dist/INSTALL.md

clean:
	rm -rf dist
