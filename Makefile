# ynr: your named reporting.
#
#   make check   everything CI checks: formatting now, and the Go checks once there is Go code
#   make fmt     format Terraform (and Go, once there is any)
#   make help    this text

.PHONY: check fmt fmt-check help

check: fmt-check

help:
	@sed -n '2,/^$$/p' Makefile | sed -e 's/^# \{0,1\}//'

fmt:
	terraform fmt -recursive infra
	@if [ -f go.mod ]; then gofmt -w .; fi

fmt-check:
	@terraform fmt -check -recursive infra || { echo "terraform fmt needed: run make fmt"; exit 1; }
	@if [ -f go.mod ]; then out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi; fi
