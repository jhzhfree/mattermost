GO ?= go
PYTHON ?= python3

.PHONY: test bundle demo interop source-contract live-preflight
test:
	$(GO) test -race -count=1 ./...
	$(PYTHON) internal/handoff/testdata/check_vectors.py

bundle:
	mkdir -p dist/plugin/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o dist/plugin/server/linux-amd64 .
	cp plugin.json dist/plugin/plugin.json
	cp README.md dist/plugin/README.md
	cp DEMO.md dist/plugin/DEMO.md
	cp OPERATIONS.md dist/plugin/OPERATIONS.md
	cp DEPLOYMENT.md dist/plugin/DEPLOYMENT.md
	cp SOURCE.md dist/plugin/SOURCE.md
	cp INTEROP.md dist/plugin/INTEROP.md
	cp PROTOCOL.md dist/plugin/PROTOCOL.md
	mkdir -p dist/plugin/protocol
	cp protocol/check_octop_contract.py protocol/octop-contract.json dist/plugin/protocol/
	cp audit-target.example.json dist/plugin/audit-target.example.json
	cp ../../../LICENSE.txt dist/plugin/LICENSE.txt
	tar -czf dist/ai-tagent-intake-0.2.16-linux-amd64.tar.gz -C dist/plugin .

# Explicit isolated scenario runner; no plugin setting enables the scheduler.
REPORT ?= dist/demo-report.json
demo:
	$(PYTHON) tests/postgres_inbox.py --go "$(GO)" --demo --report "$(REPORT)"

# Local peer objects and prepared dependencies only; never fetch/install.
interop:
	$(PYTHON) tests/octop_contract.py --octop-git-dir "$(OCTOP_GIT_DIR)" --go "$(GO)" --report "$(REPORT)"

source-contract:
	$(PYTHON) tests/source_contract.py --octop-git-dir "$(OCTOP_GIT_DIR)" --go "$(GO)" --report "$(REPORT)"

# Exit 2 is expected while live deployment gates remain unmet. No network.
live-preflight:
	$(PYTHON) tests/live_preflight.py --go "$(GO)" --probe-build --probe-isolation --report "$(REPORT)"
