// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

const OctopIntakePath = "/api/integrations/mattermost/v1/intakes"
const brokerDomain = "octop-mattermost-broker-request/v1\x00"

// SignOfflineBrokerRequest matches Octop 247b456's isolated authentication
// profile. It only signs explicitly supplied bytes with an explicitly supplied
// key; no loader, HTTP client, route or production caller is provided. The key
// MUST live in a separately isolated broker before any real deployment. A
// distinct proof public key is mandatory. Headers contain no user JWT/actor ID.
func SignOfflineBrokerRequest(broker string, key ed25519.PrivateKey, proofKey ed25519.PublicKey, method, path string, body []byte, issued int64) (map[string]string, error) {
	if !token(broker, 128) || len(key) != ed25519.PrivateKeySize || len(proofKey) != ed25519.PublicKeySize || issued <= 0 || bytes.Equal(key.Public().(ed25519.PublicKey), proofKey) {
		return nil, ErrRejected
	}
	content := ""
	limit := 0
	if method == "POST" && path == OctopIntakePath {
		content = "application/octet-stream"
		limit = MaxProofBytes
	} else if strings.HasPrefix(path, OctopIntakePath+"/") {
		rest := strings.TrimPrefix(path, OctopIntakePath+"/")
		if method == "POST" && strings.HasSuffix(rest, "/cancellations") && validKey(strings.TrimSuffix(rest, "/cancellations")) {
			content = "application/json"
			limit = 1024
		} else if method != "GET" || !validKey(rest) {
			return nil, ErrRejected
		}
	} else {
		return nil, ErrRejected
	}
	if len(body) > limit {
		return nil, ErrRejected
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrRejected
	}
	n := hex.EncodeToString(nonce[:])
	timestamp := strconv.FormatInt(issued, 10)
	digest := sha256.Sum256(body)
	message := brokerDomain + strings.Join([]string{broker, timestamp, n, method, path, hex.EncodeToString(digest[:])}, "\n")
	headers := map[string]string{"x-octop-broker": broker, "x-octop-broker-time": timestamp, "x-octop-broker-nonce": n, "x-octop-broker-signature": base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(message)))}
	if content != "" {
		headers["content-type"] = content
	}
	return headers, nil
}
