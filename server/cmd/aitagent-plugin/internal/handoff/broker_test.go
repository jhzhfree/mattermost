// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestOfflineBrokerSignerScopeAndKeySeparation(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	proof, _, _ := ed25519.GenerateKey(rand.Reader)
	a, err := SignOfflineBrokerRequest("fixture", key, proof, "POST", OctopIntakePath, []byte("synthetic"), 101)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SignOfflineBrokerRequest("fixture", key, proof, "POST", OctopIntakePath, []byte("synthetic"), 101)
	if a["x-octop-broker-nonce"] == b["x-octop-broker-nonce"] {
		t.Fatal("nonce reused")
	}
	for _, c := range []struct {
		method, path, body string
		pub                ed25519.PublicKey
	}{
		{"POST", OctopIntakePath, "", pub}, {"GET", OctopIntakePath + "/" + strings.Repeat("a", 64), "body", proof},
		{"POST", OctopIntakePath + "?actor=1", "", proof}, {"POST", "/api/chat", "", proof},
		{"DELETE", OctopIntakePath, "", proof}, {"POST", OctopIntakePath + "/" + strings.Repeat("a", 64) + "/cancellations", strings.Repeat("a", 1025), proof},
	} {
		if _, err := SignOfflineBrokerRequest("fixture", key, c.pub, c.method, c.path, []byte(c.body), 101); err == nil {
			t.Fatal("unsafe signing scope accepted")
		}
	}
}
