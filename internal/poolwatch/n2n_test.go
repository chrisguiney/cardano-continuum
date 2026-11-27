package poolwatch

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// A header forged by a pool carries the cold key whose hash is the pool's
// PoolID; the chain-sync callback must report it on the Mark.
func TestForwardCarriesIssuer(t *testing.T) {
	t.Parallel()
	raw, err := hex.DecodeString(fixture(t, "header.hex"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := conway.NewConwayBlockHeaderFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := &n2nConn{events: make(chan Event, 1), done: make(chan struct{})}
	if err := c.forward(chainsync.CallbackContext{}, 0, h, chainsync.Tip{}); err != nil {
		t.Fatal(err)
	}
	m := (<-c.events).Forward
	if got := hex.EncodeToString(m.Issuer[:]); got != fixture(t, "poolid.hex") {
		t.Fatalf("issuer %s, want %s", got, fixture(t, "poolid.hex"))
	}
	if m.Slot != 100 || m.Height != 13 {
		t.Fatalf("mark slot %d height %d, want 100 and 13", m.Slot, m.Height)
	}
}
