// See LICENSE.txt in the repository root for license information.

package handoff

import (
	"encoding/json"
	"os"
	"testing"
)

// Public synthetic vectors contain no private key or real service data.
type wireVector struct {
	Name     string
	Proof    []byte
	Policy   Policy
	Now      int64
	Accepted bool
	Expected Candidate
}

func TestPublicCrossLanguageVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []wireVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 6 {
		t.Fatal("missing boundary vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			c, err := Verify(v.Proof, v.Policy, v.Now)
			if (err == nil) != v.Accepted || c != v.Expected {
				t.Fatal("wire vector mismatch", err)
			}
		})
	}
}
