package engine

import (
	"bytes"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// TestDigestCarriesRawBlock: the block's own CBOR rides in the BlockApplied
// content, so a log reader (the explorer) can decode the whole block.
func TestDigestCarriesRawBlock(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 1, 2)
	c, err := NewFakeChain(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdvanceTo(40); err != nil {
		t.Fatal(err)
	}
	bs := c.Blocks()
	if len(bs) == 0 {
		t.Fatal("fake chain made no blocks")
	}
	content, err := Digest(bs[0].Cbor)
	if err != nil {
		t.Fatal(err)
	}
	var bc BlockContent
	if err := eventlog.Decode(content, &bc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bc.Raw, bs[0].Cbor) {
		t.Fatalf("raw block missing: %d bytes, want %d", len(bc.Raw), len(bs[0].Cbor))
	}
	if _, err := conway.NewConwayBlockFromCbor(bc.Raw); err != nil {
		t.Fatalf("raw block does not decode: %v", err)
	}
}
