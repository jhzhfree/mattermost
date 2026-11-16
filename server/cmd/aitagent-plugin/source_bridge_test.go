//go:build octopcontract

// See LICENSE.txt in the repository root for license information.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Synthetic-only pipe bridge: no socket, real key, route or runtime caller.
func TestOfflineSourceBridge(t *testing.T) {
	if os.Getenv("AITAGENT_SOURCE_BRIDGE") != "1" {
		t.Skip("isolated source contract only")
	}
	s, f, args, trust, key, id := queryFixture(t)
	emit := func(v any) {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Println("BRIDGE " + string(raw))
	}
	emit(map[string]any{"trust": trust, "receipt": id, "now": s.plugin.nowMillis() / 1000})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 32768)
	for scanner.Scan() {
		var req struct {
			Operation string
			Body      []byte
			Signature string
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			t.Fatal("invalid bridge frame")
		}
		var data []byte
		var err error
		switch req.Operation {
		case "sign":
			if !bytes.HasPrefix(req.Body, []byte(sourceQueryDomain)) {
				t.Fatal("wrong signing purpose")
			}
			data = ed25519.Sign(key, req.Body)
		case "query":
			data, err = callQuery(s, req.Body, req.Signature)
		case "cancel":
			_, err = s.plugin.lookup(id, f.config, args, true)
		case "revoke":
			f.permission = false
		case "restore":
			f.permission = true
		case "registry_revoke":
			trust.Enabled = false
			s.resolve = func(context.Context) (sourceQueryTrust, error) { return trust, nil }
		default:
			t.Fatal("unknown bridge operation")
		}
		emit(map[string]any{"data": data, "rejected": err != nil})
	}
	if scanner.Err() != nil {
		t.Fatal(scanner.Err())
	}
}
