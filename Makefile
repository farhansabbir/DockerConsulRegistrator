# docker-consul-registrator - every task has a target here.
#
#   make check                  format, vet, tests (race), lint, vulnerabilities, docs
#   make docs-check             the documentation page (published by .github/workflows/pages.yml)
#   make docs-serve             the documentation page at http://127.0.0.1:8000
#   make build                  dist/docker-consul-registrator for this machine
#   make dist                   static Linux binaries (amd64, arm64)
#   make image                  the container image (needs Docker)
#   make remote-check REMOTE=h  the same checks on a Linux machine over ssh

BIN      := docker-consul-registrator
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath
IMAGE    ?= docker-consul-registrator:$(VERSION)
REMOTE_DIR ?= dcr
REMOTE_ENV ?= PATH=$$HOME/.local/go/bin:$$HOME/go/bin:$$PATH

.PHONY: check docs-check docs-serve build dist image remote-sync remote-check clean

check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || { echo "gofmt: the files above are not formatted"; exit 1; }
	@! find . -name '*.go' -exec perl -ne 'if (/[^\x00-\x7F]/) { print "$$ARGV:$$.: not ASCII\n"; $$bad=1 } END { exit !$$bad }' {} \; | grep .
	go vet ./...
	go test -race -count=1 ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "golangci-lint not installed; skipped"; fi
	@if command -v govulncheck >/dev/null; then govulncheck ./...; else echo "govulncheck not installed; skipped"; fi
	@$(MAKE) --no-print-directory docs-check

docs-check:
	python3 -I scripts/docs_check.py docs/index.html

docs-serve:
	python3 -m http.server --bind 127.0.0.1 --directory docs 8000

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BIN) ./cmd/$(BIN)

dist:
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BIN)-linux-$$arch ./cmd/$(BIN) || exit 1; \
	done

image: dist
	docker build -t $(IMAGE) .

remote-sync:
	@test -n "$(REMOTE)" || { echo "REMOTE=<ssh host> is needed"; exit 2; }
	rsync -az --delete --exclude .git --exclude dist ./ $(REMOTE):$(REMOTE_DIR)/

remote-check: remote-sync
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && $(REMOTE_ENV) make check VERSION=$(VERSION)'

clean:
	rm -f dist/$(BIN) dist/$(BIN)-linux-*
