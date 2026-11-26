package phase

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

func fixtureText(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../poolwatch/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(raw)))
}

// The hash of the cold key in a pool's operational certificate is the issuer
// hash of the headers that pool forges.
func TestIssuerMatchesHeader(t *testing.T) {
	raw, err := hex.DecodeString(string(fixtureText(t, "header.hex")))
	if err != nil {
		t.Fatal(err)
	}
	h, err := conway.NewConwayBlockHeaderFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := IssuerOf("continuum-pool-0", map[string][]byte{render.OpCert: fixtureText(t, "opcert.cert")})
	if err != nil {
		t.Fatal(err)
	}
	if got != h.IssuerVkey().Hash() {
		t.Fatalf("issuer %x, header says %x", got, h.IssuerVkey().Hash())
	}
}

func TestIssuerOfHexPoolID(t *testing.T) {
	id := string(fixtureText(t, "poolid.hex"))
	got, err := IssuerOf(id, nil)
	if err != nil || hex.EncodeToString(got[:]) != id {
		t.Fatalf("issuer %x, %v; want %s", got, err, id)
	}
}

func TestIssuerErrors(t *testing.T) {
	for name, files := range map[string]map[string][]byte{
		"no certificate":  {},
		"not an envelope": {render.OpCert: []byte("opcert")},
		"blank secret":    {render.OpCert: []byte{}},
		"bad cbor":        {render.OpCert: []byte(`{"cborHex":"00"}`)},
	} {
		if _, err := IssuerOf("continuum-pool-0", files); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
