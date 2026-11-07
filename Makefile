PYTHON ?= python3
GO ?= go
GOFMT ?= gofmt
PNPM ?= pnpm

.PHONY: generate generated-check check contracts-check naming-check backend-test integration-test web-check browser-test

generate:
	$(PYTHON) tooling/codegen/generate.py
	$(GOFMT) -w backend/internal/gen/wire
	$(PNPM) generate:api

generated-check: generate
	git diff --exit-code -- backend/internal/gen/wire packages/api-client/src/schema.gen.ts

check: naming-check contracts-check backend-test web-check

naming-check:
	$(PYTHON) tooling/check_naming.py

contracts-check:
	$(PYTHON) tooling/verify_contracts.py

backend-test:
	cd backend && formatted="$$($(GOFMT) -l .)" && test -z "$$formatted" || { printf '%s\n' "$$formatted"; exit 1; }
	cd backend && $(GO) vet ./...
	cd backend && $(GO) test -race ./...

integration-test:
	@test -n "$$EVIA_TEST_DATABASE_URL" || { echo 'EVIA_TEST_DATABASE_URL is required'; exit 1; }
	cd backend && $(GO) test -race -tags=integration ./internal/storage/postgres -count=1

web-check:
	$(PNPM) format:check
	$(PNPM) typecheck
	$(PNPM) build

browser-test:
	@test -n "$$EVIA_E2E_USERNAME" && test -n "$$EVIA_E2E_PASSWORD" || { echo 'EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD are required'; exit 1; }
	$(PNPM) test:browser
