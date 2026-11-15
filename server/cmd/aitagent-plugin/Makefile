GO ?= go

.PHONY: test bundle
test:
	$(GO) test -race -count=1 ./...

bundle:
	mkdir -p dist/plugin/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o dist/plugin/server/linux-amd64 .
	cp plugin.json dist/plugin/plugin.json
	cp README.md dist/plugin/README.md
	cp audit-target.example.json dist/plugin/audit-target.example.json
	cp ../../../LICENSE.txt dist/plugin/LICENSE.txt
	tar -czf dist/ai-tagent-intake-0.2.3-linux-amd64.tar.gz -C dist/plugin .
