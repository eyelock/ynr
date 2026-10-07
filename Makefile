# ynr: your named reporting.
#
#   make check     everything CI checks: formatting, vet, lint and tests with the race detector,
#                  for ynr, the Go spool exporter and the npm spool exporter
#   make js        install and build the npm spool exporter (spoolexporter/js)
#   make build     the slim build into bin/ynr (no cgo), plus linux builds for images
#   make full      the full build, with DuckDB (cgo), into bin/ynr-full
#   make generate  the dashboard's templates into Go
#   make test      the tests
#   make fmt       format Go and Terraform
#   make help      this text

VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/eyelock/ynr.Version=$(VERSION)

# Go modules in this repository: ynr, and the spool exporter the other tools import.
MODULES := . spoolexporter

# The npm spool exporter.
JS := spoolexporter/js

.PHONY: check build full test vet lint fmt fmt-check slim-check generate generate-check help clean js js-test

check: fmt-check generate-check vet lint slim-check js js-test test

# The dashboard's templ templates, generated into Go and committed (ADR-005).
TEMPL := go run github.com/a-h/templ/cmd/templ@v0.3.1070

generate:
	$(TEMPL) generate -path internal/ui

generate-check:
	@$(TEMPL) generate -path internal/ui >/dev/null 2>&1
	@git diff --quiet -- 'internal/ui/*_templ.go' || { echo "generated templates are stale: run make generate and commit"; git diff --stat -- 'internal/ui/*_templ.go'; exit 1; }

help:
	@sed -n '2,/^$$/p' Makefile | sed -e 's/^# \{0,1\}//'

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr ./cmd/ynr
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr-linux-arm64 ./cmd/ynr
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr-linux-amd64 ./cmd/ynr

full:
	CGO_ENABLED=1 go build -tags full -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr-full ./cmd/ynr

# Each module's tests, and ynr's again as the full build (ADR-001).
test:
	@for m in $(MODULES); do (cd $$m && go test -race -count=1 ./...) || exit 1; done
	go test -race -count=1 -tags full ./...

vet:
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done
	go vet -tags full ./...

lint:
	@for m in $(MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done
	golangci-lint run --build-tags full ./...

# The slim build must build without cgo and never link DuckDB.
slim-check:
	CGO_ENABLED=0 go build -o /dev/null ./cmd/ynr
	@if go list -deps ./cmd/ynr | grep -q duckdb; then echo "the slim build links DuckDB"; exit 1; fi

# Built before the Go tests, which run it to prove ynr reads what it writes.
js:
	cd $(JS) && npm ci --no-audit --no-fund && npm run build

js-test:
	cd $(JS) && npm run typecheck && npm test

fmt:
	gofmt -w .
	terraform fmt -recursive infra

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@terraform fmt -check -recursive infra || { echo "terraform fmt needed: run make fmt"; exit 1; }

clean:
	rm -rf bin $(JS)/dist $(JS)/node_modules
