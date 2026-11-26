package forger

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// TestNodeServesItsChain runs a forger that leads every slot with no
// upstream, syncs it with a gouroboros node-to-node client, and requires
// each block served to pass gouroboros's block verification.
func TestNodeServesItsChain(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	p := testParams()
	p.ActiveSlots = big.NewRat(1, 1)
	p.SlotLength = 50 * time.Millisecond
	p.SystemStart = time.Now().Add(100 * time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	n, err := New(Config{Params: p, Creds: testCreds(t), Listener: ln, EvidenceDir: dir},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()

	const want = 4
	var mu sync.Mutex // protects headers
	var headers []ledger.BlockHeader
	got := make(chan struct{})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn), ouroboros.WithNetworkMagic(p.Magic), ouroboros.WithNodeToNode(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollForwardFunc(func(_ chainsync.CallbackContext, _ uint, h any, _ chainsync.Tip) error {
				mu.Lock()
				defer mu.Unlock()
				headers = append(headers, h.(ledger.BlockHeader))
				if len(headers) == want {
					close(got)
				}
				return nil
			}),
			chainsync.WithRollBackwardFunc(func(chainsync.CallbackContext, pcommon.Point, chainsync.Tip) error { return nil }),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := oc.ChainSync().Client.Sync(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case err := <-done:
		t.Fatalf("node stopped: %v", err)
	case err := <-oc.ErrorChan():
		t.Fatalf("client connection failed: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("no blocks served in 20s")
	}
	mu.Lock()
	served := append([]ledger.BlockHeader(nil), headers[:want]...)
	mu.Unlock()
	nonces := GenesisNonces(p)
	for i, h := range served {
		blk, err := oc.BlockFetch().Client.GetBlock(pcommon.NewPoint(h.SlotNumber(), h.Hash().Bytes()))
		if err != nil {
			t.Fatalf("fetch block %d: %v", i, err)
		}
		eta := nonces.Tick(p, h.SlotNumber()).EpochNonce()
		if _, _, _, _, err := ledger.VerifyBlock(blk, hex.EncodeToString(eta[:]), p.SlotsPerKESPeriod, common.VerifyConfig{
			SkipTransactionValidation: true, SkipStakePoolValidation: true, SkipBlockLimitsValidation: true,
			MaxKesEvolutions: p.MaxKESEvolutions,
		}); err != nil {
			t.Fatalf("served block %d at slot %d is invalid: %v", i, h.SlotNumber(), err)
		}
		if i > 0 && h.PrevHash() != served[i-1].Hash() {
			t.Fatalf("block %d does not extend block %d", i, i-1)
		}
		b, _ := n.chain.Block(h.Hash())
		nonces = nonces.Apply(p, b.Slot, b.Prev, b.VRFOutput)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*-honest-*.block")); len(files) < want {
		t.Errorf("evidence directory holds %d honest blocks, want at least %d", len(files), want)
	}
	_ = oc.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestGenesisGuard renders a devnet, loads it as the forger does, and
// requires a genesis file that is not the pinned one to be refused.
func TestGenesisGuard(t *testing.T) {
	store, err := keys.New(rng.Seed{3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := genesis.Default()
	s.SystemStart = time.Unix(1_800_000_000, 0).UTC()
	bundle, err := genesis.Render(s, store)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, bundle); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config")
	c, err := LoadCredentials(filepath.Join(dir, "pools", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if c.PoolID != bundle.Pools[1].ID {
		t.Fatalf("pool id %x, genesis says %x", c.PoolID, bundle.Pools[1].ID)
	}
	if strings.Contains(c.String()+slog.AnyValue(c).String(), hex.EncodeToString(c.vrfSeed)) {
		t.Fatal("credentials print their VRF key")
	}
	p, err := LoadGenesis(cfg, c.PoolID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Magic != s.Magic || p.EpochLength != uint64(s.EpochLength) || p.SlotLength != s.SlotLength ||
		!p.SystemStart.Equal(s.SystemStart) || p.ActiveSlots.Cmp(big.NewRat(1, 20)) != 0 {
		t.Errorf("params = %+v", p)
	}
	if p.PoolStake != s.PoolStake || p.TotalStake != uint64(s.Pools)*s.PoolStake {
		t.Errorf("stake = %d of %d, want %d of %d", p.PoolStake, p.TotalStake, s.PoolStake, uint64(s.Pools)*s.PoolStake)
	}
	if hex.EncodeToString(p.GenesisHash[:]) != bundle.Hashes.Shelley {
		t.Errorf("genesis hash %x, want %s", p.GenesisHash, bundle.Hashes.Shelley)
	}

	file := filepath.Join(cfg, genesis.ShelleyFile)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(raw, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGenesis(cfg, c.PoolID); !errors.Is(err, ErrGenesisMismatch) {
		t.Fatalf("a changed genesis file loaded: %v", err)
	}
}
