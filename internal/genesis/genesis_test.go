package genesis

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/byron"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

var dump = flag.String("dump", "", "write the seed-1 bundle to this directory, for recording hashes.golden")

func seed(b byte) rng.Seed {
	var s rng.Seed
	s[31] = b
	return s
}

func store(t *testing.T, b byte) *keys.Store {
	t.Helper()
	k, err := keys.New(seed(b), 0)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// fixedSpec is Default with a pinned start time, so outputs are comparable
// across runs and against the recorded hashes.
func fixedSpec() Spec {
	s := Default()
	s.SystemStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return s
}

func render(t *testing.T, s Spec, b byte) Bundle {
	t.Helper()
	out, err := Render(s, store(t, b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCheck(t *testing.T) {
	t.Parallel()
	ok := Default()
	tests := []struct {
		name string
		mod  func(*Spec)
		want string // substring of the error; empty means valid
	}{
		{"default", func(*Spec) {}, ""},
		{"k5 f0.05 e1000", func(s *Spec) { s.EpochLength = 1000 }, ""},
		{"k5 f0.25 e100", func(s *Spec) { s.ActiveSlotsCoeff = 0.25; s.EpochLength = 100 }, ""},
		{"inverse f not integer", func(s *Spec) { s.SecurityParam = 40; s.ActiveSlotsCoeff = 0.4 }, "1/f"},
		{"not a multiple of k/f", func(s *Spec) { s.EpochLength = 450 }, "k/f"},
		{"inside 4k/f", func(s *Spec) { s.EpochLength = 400 }, "4k/f"},
		{"f zero", func(s *Spec) { s.ActiveSlotsCoeff = 0 }, "activeSlotsCoeff"},
		{"f above one", func(s *Spec) { s.ActiveSlotsCoeff = 2 }, "activeSlotsCoeff"},
		{"k zero", func(s *Spec) { s.SecurityParam = 0 }, "securityParam"},
		{"no pools", func(s *Spec) { s.Pools = 0 }, "pools"},
		{"no committee", func(s *Spec) { s.Committee = 0 }, "committee"},
		{"pre-Conway", func(s *Spec) { s.ProtocolVersion = [2]uint{8, 0} }, "protocol version"},
		{"oversubscribed", func(s *Spec) { s.MaxSupply = s.FaucetLovelace }, "maxSupply"},
		{"sub-millisecond slot", func(s *Spec) { s.SlotLength = 1500 * time.Microsecond }, "slotLength"},
		{"no magic", func(s *Spec) { s.Magic = 0 }, "magic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := ok
			tt.mod(&s)
			err := Check(s)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("Check = %v, want nil", err)
			case tt.want != "" && err == nil:
				t.Fatalf("Check = nil, want error naming %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("Check = %v, want error naming %q", err, tt.want)
			}
		})
	}
}

func TestRenderRejectsInvalidSpec(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	s.EpochLength = 450
	if _, err := Render(s, store(t, 1)); err == nil {
		t.Fatal("Render accepted a spec Check rejects")
	}
}

func TestRenderDeterministic(t *testing.T) {
	t.Parallel()
	a, b := render(t, fixedSpec(), 1), render(t, fixedSpec(), 1)
	if !bundleEqual(a, b) {
		t.Fatal("same spec and seed gave different bundles")
	}
	c := render(t, fixedSpec(), 2)
	if bytes.Equal(a.Shelley, c.Shelley) || a.Hashes == c.Hashes {
		t.Fatal("different seeds gave the same Shelley genesis")
	}
}

func bundleEqual(a, b Bundle) bool {
	if !bytes.Equal(a.Byron, b.Byron) || !bytes.Equal(a.Shelley, b.Shelley) ||
		!bytes.Equal(a.Alonzo, b.Alonzo) || !bytes.Equal(a.Conway, b.Conway) ||
		!bytes.Equal(a.NodeConfig, b.NodeConfig) || a.Hashes != b.Hashes ||
		a.SpecHash != b.SpecHash || len(a.Pools) != len(b.Pools) {
		return false
	}
	for i := range a.Pools {
		pa, pb := a.Pools[i], b.Pools[i]
		if pa.ID != pb.ID || !bytes.Equal(pa.Topology, pb.Topology) || len(pa.Files) != len(pb.Files) {
			return false
		}
		for name, data := range pa.Files {
			if !bytes.Equal(data, pb.Files[name]) {
				return false
			}
		}
	}
	return true
}

// TestHashesGolden pins the hashes of the seed-1 bundle. testdata/hashes.golden
// was produced by cardano-cli 11.2.3.1 (ghcr.io/blinklabs-io/cardano-node:11.1.3-1),
// not by this package: `byron genesis print-genesis-hash` and
// `hash genesis-file` over the files written by -dump, as in
// testdata/record-hashes.sh.
// Not parallel: writes the bundle to -dump.
func TestHashesGolden(t *testing.T) {
	b := render(t, fixedSpec(), 1)
	if *dump != "" {
		if _, err := WriteDir(*dump, b); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "hashes.golden"))
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("byron %s\nshelley %s\nalonzo %s\nconway %s\n",
		b.Hashes.Byron, b.Hashes.Shelley, b.Hashes.Alonzo, b.Hashes.Conway)
	if string(want) != got {
		t.Fatalf("hashes differ from cardano-cli\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// TestByronHashIsCanonical checks that the Byron hash does not depend on the
// file's formatting: reindenting and reordering keys must keep it, as
// cardano-node hashes the canonical rendering, not the bytes.
func TestByronHashIsCanonical(t *testing.T) {
	t.Parallel()
	b := render(t, fixedSpec(), 1)
	var v map[string]any
	dec := json.NewDecoder(bytes.NewReader(b.Byron))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	pretty, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ByronHash(pretty)
	if err != nil {
		t.Fatal(err)
	}
	if h != b.Hashes.Byron {
		t.Fatalf("ByronHash(reformatted) = %s, want %s", h, b.Hashes.Byron)
	}
	if fileHash := hex.EncodeToString(blake(b.Byron)); fileHash != b.Hashes.Byron {
		t.Fatalf("rendered Byron file is not canonical: file hash %s, canonical %s", fileHash, b.Hashes.Byron)
	}
}

func blake(b []byte) []byte {
	h := common.Blake2b256Hash(b)
	return h[:]
}

func TestFileHashes(t *testing.T) {
	t.Parallel()
	b := render(t, fixedSpec(), 1)
	for name, c := range map[string]struct {
		file []byte
		hash string
	}{
		"shelley": {b.Shelley, b.Hashes.Shelley},
		"alonzo":  {b.Alonzo, b.Hashes.Alonzo},
		"conway":  {b.Conway, b.Hashes.Conway},
	} {
		if got := hex.EncodeToString(blake(c.file)); got != c.hash {
			t.Errorf("%s hash = %s, want Blake2b-256 of file %s", name, c.hash, got)
		}
	}
}

func TestParsesWithGouroboros(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	b := render(t, s, 1)

	by, err := byron.NewByronGenesisFromReader(bytes.NewReader(b.Byron))
	if err != nil {
		t.Fatalf("byron: %v", err)
	}
	if by.ProtocolConsts.K != int(s.SecurityParam) || by.ProtocolConsts.ProtocolMagic != int(s.Magic) ||
		by.StartTime != int(s.SystemStart.Unix()) {
		t.Errorf("byron consts = %+v start %d", by.ProtocolConsts, by.StartTime)
	}

	sh, err := shelley.NewShelleyGenesisFromReader(bytes.NewReader(b.Shelley))
	if err != nil {
		t.Fatalf("shelley: %v", err)
	}
	if sh.EpochLength != int(s.EpochLength) || sh.SecurityParam != int(s.SecurityParam) ||
		sh.NetworkMagic != s.Magic || !sh.SystemStart.Equal(s.SystemStart) ||
		sh.MaxLovelaceSupply != s.MaxSupply {
		t.Errorf("shelley = epoch %d k %d magic %d start %s supply %d",
			sh.EpochLength, sh.SecurityParam, sh.NetworkMagic, sh.SystemStart, sh.MaxLovelaceSupply)
	}
	if f, _ := sh.ActiveSlotsCoeff.Float64(); f != s.ActiveSlotsCoeff {
		t.Errorf("activeSlotsCoeff = %v", f)
	}
	pv := sh.ProtocolParameters.ProtocolVersion
	if [2]uint{pv.Major, pv.Minor} != s.ProtocolVersion {
		t.Errorf("protocol version = %+v", pv)
	}
	if d, _ := sh.ProtocolParameters.Decentralization.Float64(); d != 0 {
		t.Errorf("decentralisationParam = %v, want 0 so pools forge every slot", d)
	}

	if _, err := alonzo.NewAlonzoGenesisFromReader(bytes.NewReader(b.Alonzo)); err != nil {
		t.Fatalf("alonzo: %v", err)
	}
	if _, err := conway.NewConwayGenesisFromReader(bytes.NewReader(b.Conway)); err != nil {
		t.Fatalf("conway: %v", err)
	}
}

func TestGenesisPoolsStaked(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	k := store(t, 1)
	b, err := Render(s, k)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := shelley.NewShelleyGenesisFromReader(bytes.NewReader(b.Shelley))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Staking.Pools) != s.Pools || len(b.Pools) != s.Pools {
		t.Fatalf("genesis has %d pools, bundle %d, want %d", len(sh.Staking.Pools), len(b.Pools), s.Pools)
	}
	staked := map[string]uint64{}
	for addrHex, amount := range sh.InitialFunds {
		raw, err := hex.DecodeString(addrHex)
		if err != nil {
			t.Fatal(err)
		}
		a, err := common.NewAddressFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		sk := a.StakeKeyHash()
		if pool, ok := sh.Staking.Stake[hex.EncodeToString(sk[:])]; ok {
			staked[pool] += amount
		}
	}
	var total uint64
	for i := range s.Pools {
		want, err := k.Pool(keys.ActorID(i), 0)
		if err != nil {
			t.Fatal(err)
		}
		id := hex.EncodeToString(want.PoolID[:])
		reg, ok := sh.Staking.Pools[id]
		if !ok {
			t.Fatalf("pool %d id %s missing from staking.pools", i, id)
		}
		if reg.VrfKeyHash != common.Blake2b256(want.VRFVKeyHash) {
			t.Errorf("pool %d vrf = %x, want %x", i, reg.VrfKeyHash, want.VRFVKeyHash)
		}
		if b.Pools[i].ID != want.PoolID {
			t.Errorf("bundle pool %d id = %x, want %x", i, b.Pools[i].ID, want.PoolID)
		}
		if staked[id] != s.PoolStake {
			t.Errorf("pool %d staked %d, want %d", i, staked[id], s.PoolStake)
		}
		total += staked[id]
	}
	var funds uint64
	for _, v := range sh.InitialFunds {
		funds += v
	}
	if funds != total+s.FaucetLovelace {
		t.Errorf("initial funds %d = staked %d + faucet %d does not hold", funds, total, s.FaucetLovelace)
	}
	if funds > s.MaxSupply {
		t.Errorf("initial funds %d exceed max supply %d", funds, s.MaxSupply)
	}
}

func TestFaucetFunded(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	k := store(t, 1)
	b, err := Render(s, k)
	if err != nil {
		t.Fatal(err)
	}
	want := k.Faucet().Address()
	if b.Faucet.String() != want.String() {
		t.Fatalf("bundle faucet = %s, want %s", b.Faucet, want)
	}
	raw, err := want.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sh, err := shelley.NewShelleyGenesisFromReader(bytes.NewReader(b.Shelley))
	if err != nil {
		t.Fatal(err)
	}
	if got := sh.InitialFunds[hex.EncodeToString(raw)]; got != s.FaucetLovelace {
		t.Fatalf("faucet funds = %d, want %d", got, s.FaucetLovelace)
	}
}

func TestCommitteeAndConstitution(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	k := store(t, 1)
	b, err := Render(s, k)
	if err != nil {
		t.Fatal(err)
	}
	cg, err := conway.NewConwayGenesisFromReader(bytes.NewReader(b.Conway))
	if err != nil {
		t.Fatal(err)
	}
	if len(cg.Committee.Members) != s.Committee {
		t.Fatalf("committee has %d members, want %d", len(cg.Committee.Members), s.Committee)
	}
	for i := range s.Committee {
		cold, _ := k.Committee(uint32(i))
		key := "keyHash-" + hex.EncodeToString(cold[:])
		if _, ok := cg.Committee.Members[key]; !ok {
			t.Errorf("committee member %d (%s) missing", i, key)
		}
	}
	if cg.Committee.Threshold == nil || cg.Committee.Threshold.Sign() <= 0 {
		t.Errorf("committee threshold = %v, want positive", cg.Committee.Threshold)
	}
	if len(cg.InitialDReps) != 0 {
		t.Errorf("initial dreps = %d, want a DRep-less start", len(cg.InitialDReps))
	}
	if cg.Constitution.Script != "" {
		t.Errorf("constitution guardrail script = %q, want none", cg.Constitution.Script)
	}
}

func TestPoolFiles(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	k := store(t, 1)
	b, err := Render(s, k)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range b.Pools {
		names := make([]string, 0, len(p.Files))
		for n := range p.Files {
			names = append(names, n)
		}
		for _, n := range []string{"kes.skey", "vrf.skey", "opcert.cert", "cold.vkey"} {
			if len(p.Files[n]) == 0 {
				t.Errorf("pool %d missing %s (has %v)", i, n, names)
			}
		}
		for n := range p.Files {
			if strings.Contains(n, "cold.skey") {
				t.Errorf("pool %d ships its cold signing key", i)
			}
		}
		want, err := k.OpCert(keys.ActorID(i), 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var env struct{ Type, CborHex string }
		if err := json.Unmarshal(p.Files["opcert.cert"], &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != "NodeOperationalCertificate" || env.CborHex != hex.EncodeToString(want) {
			t.Errorf("pool %d opcert = %s %s..., want keys.OpCert", i, env.Type, env.CborHex[:16])
		}
		pk, err := k.Pool(keys.ActorID(i), 0)
		if err != nil {
			t.Fatal(err)
		}
		kes, err := pk.KESSKey.TextEnvelope()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.Files["kes.skey"], kes) {
			t.Errorf("pool %d kes.skey differs from keys.Pool", i)
		}
		if err := json.Unmarshal(p.Files["cold.vkey"], &env); err != nil {
			t.Fatal(err)
		}
		if env.CborHex != "5820"+hex.EncodeToString(pk.ColdVKey[:]) {
			t.Errorf("pool %d cold.vkey = %s", i, env.CborHex)
		}
	}
}

func TestTopology(t *testing.T) {
	t.Parallel()
	s := fixedSpec()
	b := render(t, s, 1)
	for i, p := range b.Pools {
		var topo struct {
			LocalRoots []struct {
				AccessPoints []struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"accessPoints"`
				Valency int `json:"valency"`
			} `json:"localRoots"`
		}
		if err := json.Unmarshal(p.Topology, &topo); err != nil {
			t.Fatal(err)
		}
		aps := topo.LocalRoots[0].AccessPoints
		if len(aps) != s.Pools-1 || topo.LocalRoots[0].Valency != s.Pools-1 {
			t.Fatalf("pool %d has %d peers, want %d", i, len(aps), s.Pools-1)
		}
		for _, ap := range aps {
			if ap.Address == fmt.Sprintf(s.PoolHost, i) {
				t.Errorf("pool %d peers with itself", i)
			}
		}
	}
}

func TestNodeConfig(t *testing.T) {
	t.Parallel()
	b := render(t, fixedSpec(), 1)
	var cfg map[string]any
	if err := json.Unmarshal(b.NodeConfig, &cfg); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"Protocol":                   "Cardano",
		"ByronGenesisFile":           "byron-genesis.json",
		"ByronGenesisHash":           b.Hashes.Byron,
		"ShelleyGenesisFile":         "shelley-genesis.json",
		"ShelleyGenesisHash":         b.Hashes.Shelley,
		"AlonzoGenesisFile":          "alonzo-genesis.json",
		"AlonzoGenesisHash":          b.Hashes.Alonzo,
		"ConwayGenesisFile":          "conway-genesis.json",
		"ConwayGenesisHash":          b.Hashes.Conway,
		"RequiresNetworkMagic":       "RequiresMagic",
		"TestShelleyHardForkAtEpoch": 0.0,
		"TestConwayHardForkAtEpoch":  0.0,
	} {
		if cfg[key] != want {
			t.Errorf("config %s = %v, want %v", key, cfg[key], want)
		}
	}
}

func TestWriteDirIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b := render(t, fixedSpec(), 1)
	wrote, err := WriteDir(dir, b)
	if err != nil || !wrote {
		t.Fatalf("first WriteDir = %v, %v", wrote, err)
	}
	shelleyPath := filepath.Join(dir, "config", "shelley-genesis.json")
	first, err := os.ReadFile(shelleyPath)
	if err != nil {
		t.Fatal(err)
	}

	later := fixedSpec()
	later.SystemStart = later.SystemStart.Add(time.Hour)
	b2 := render(t, later, 1)
	if b2.SpecHash != b.SpecHash {
		t.Fatal("spec hash depends on systemStart")
	}
	wrote, err = WriteDir(dir, b2)
	if err != nil || wrote {
		t.Fatalf("second WriteDir = %v, %v; want no write", wrote, err)
	}
	again, err := os.ReadFile(shelleyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("second WriteDir changed the Shelley genesis")
	}

	other := fixedSpec()
	other.Pools = 2
	if _, err := WriteDir(dir, render(t, other, 1)); err == nil {
		t.Fatal("WriteDir over a different spec succeeded")
	}

	info, err := os.Stat(filepath.Join(dir, "pools", "0", "kes.skey"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("kes.skey mode = %v, want owner-only", info.Mode().Perm())
	}
}
