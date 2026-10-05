# ynr: your named reporting.
#
#   make check     everything CI checks: formatting, vet, lint and tests with the race detector
#   make build     the slim build into bin/ynr (no cgo), plus linux builds for images
#   make test      the tests
#   make fmt       format Go and Terraform
#   make help      this text

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/eyelock/ynr.Version=$(VERSION)

.PHONY: check build test vet lint fmt fmt-check help clean

check: fmt-check vet lint test

help:
	@sed -n '2,/^$$/p' Makefile | sed -e 's/^# \{0,1\}//'

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr ./cmd/ynr
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr-linux-arm64 ./cmd/ynr
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynr-linux-amd64 ./cmd/ynr

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	terraform fmt -recursive infra

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@terraform fmt -check -recursive infra || { echo "terraform fmt needed: run make fmt"; exit 1; }

clean:
	rm -rf bin
