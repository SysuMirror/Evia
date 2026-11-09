PYTHON ?= python3
GO ?= go
GOFMT ?= gofmt
PNPM ?= pnpm

.PHONY: generate generated-check check contracts-check naming-check boundaries-check benchmark-tools-test integration-assets-test backend-test integration-test pipeline-test budget-test query-test web-check browser-test

generate:
	$(PYTHON) tooling/codegen/generate.py
	$(GOFMT) -w backend/internal/gen/wire
	$(PNPM) generate:api

generated-check: generate
	git diff --exit-code -- backend/internal/gen/wire packages/api-client/src/schema.gen.ts

check: naming-check contracts-check boundaries-check benchmark-tools-test integration-assets-test backend-test web-check

naming-check:
	$(PYTHON) tooling/check_naming.py

boundaries-check:
	$(PNPM) exec node --test tooling/check_boundaries.test.cjs
	$(PNPM) exec node tooling/check_boundaries.cjs

benchmark-tools-test:
	$(PYTHON) -m unittest discover -s tooling/benchmark -p 'test_*.py'

integration-assets-test:
	$(PYTHON) -m unittest discover -s integrations/tests -p 'test_*.py'

contracts-check:
	$(PYTHON) tooling/verify_contracts.py

backend-test:
	cd backend && formatted="$$($(GOFMT) -l .)" && test -z "$$formatted" || { printf '%s\n' "$$formatted"; exit 1; }
	cd backend && $(GO) vet ./...
	cd backend && $(GO) test -race ./...

integration-test:
	@test -n "$$EVIA_TEST_DATABASE_URL" || { echo 'EVIA_TEST_DATABASE_URL is required'; exit 1; }
	cd backend && $(GO) test -race -tags=integration ./internal/storage/postgres -count=1

pipeline-test:
	@test -n "$$EVIA_TEST_KAFKA_BROKERS" && test -n "$$EVIA_TEST_CLICKHOUSE_URL" || { echo 'EVIA_TEST_KAFKA_BROKERS and EVIA_TEST_CLICKHOUSE_URL are required'; exit 1; }
	cd backend && $(GO) test -race -tags=integration ./internal/pipeline -count=1

budget-test:
	@test -n "$$EVIA_TEST_REDIS_URL" || { echo 'EVIA_TEST_REDIS_URL is required'; exit 1; }
	cd backend && $(GO) test -race -tags=integration ./internal/platform/budget -count=1

query-test:
	@test -n "$$EVIA_TEST_CLICKHOUSE_URL" && test -n "$$EVIA_TEST_CLICKHOUSE_USERNAME" && test -n "$$EVIA_TEST_CLICKHOUSE_PASSWORD" || { echo 'EVIA_TEST_CLICKHOUSE_URL, EVIA_TEST_CLICKHOUSE_USERNAME and EVIA_TEST_CLICKHOUSE_PASSWORD are required'; exit 1; }
	cd backend && $(GO) test -race -tags=integration ./internal/analysis -count=1

web-check:
	$(PNPM) format:check
	$(PNPM) typecheck
	$(PNPM) test:client
	$(PNPM) test:sdk
	$(PNPM) build:sdk
	$(PNPM) build

browser-test:
	@test -n "$$EVIA_E2E_USERNAME" && test -n "$$EVIA_E2E_PASSWORD" || { echo 'EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD are required'; exit 1; }
	$(PNPM) test:browser
