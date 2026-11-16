GO ?= go
PYTHON ?= python3

.PHONY: test bundle demo
test:
	$(GO) test -race -count=1 ./...
	$(PYTHON) internal/handoff/testdata/check_vectors.py

bundle:
	mkdir -p dist/plugin/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o dist/plugin/server/linux-amd64 .
	cp plugin.json dist/plugin/plugin.json
	cp README.md dist/plugin/README.md
	cp DEMO.md dist/plugin/DEMO.md
	cp audit-target.example.json dist/plugin/audit-target.example.json
	cp ../../../LICENSE.txt dist/plugin/LICENSE.txt
	tar -czf dist/ai-tagent-intake-0.2.11-linux-amd64.tar.gz -C dist/plugin .

# Explicit isolated scenario runner; no plugin setting enables the scheduler.
REPORT ?= dist/demo-report.json
demo:
	$(PYTHON) tests/postgres_inbox.py --go "$(GO)" --demo --report "$(REPORT)"
