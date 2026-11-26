// Package genesis renders the genesis files and node configuration of a
// blank Conway devnet from a Spec and the run's keystore, and writes them to
// a directory.
//
// Every key comes from internal/keys for the run seed: pool i is actor i
// (its cold, VRF and KES keys and its owner wallet), the faucet is
// keys.Store.Faucet and committee member i is keys.Store.Committee(i). So the
// engine can spend genesis funds and operate the genesis pools with no key
// import. Render is pure; WriteDir is the only I/O.
package genesis

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Hashes are the genesis hashes as cardano-node computes them, hex encoded.
type Hashes struct {
	Byron, Shelley, Alonzo, Conway string
}

// Pool is one genesis pool's node files. Files holds the text envelopes a
// cardano-node block producer reads (kes.skey, vrf.skey, opcert.cert) and
// cold.vkey; the cold signing key never leaves internal/keys.
type Pool struct {
	ID       [28]byte
	Topology []byte
	Files    map[string][]byte
}

// Bundle is a rendered devnet. SpecHash identifies the Spec without its
// SystemStart.
type Bundle struct {
	Byron, Shelley, Alonzo, Conway, NodeConfig []byte
	Hashes                                     Hashes
	Pools                                      []Pool
	Faucet                                     common.Address
	SpecHash                                   string
}

// Render builds the devnet for s from k. The same s and seed give
// byte-identical output. k must be a testnet (network id 0) store.
func Render(s Spec, k *keys.Store) (Bundle, error) {
	if err := Check(s); err != nil {
		return Bundle{}, fmt.Errorf("genesis spec: %w", err)
	}
	faucet := k.Faucet()
	if faucet.Address().NetworkId() != 0 {
		return Bundle{}, fmt.Errorf("keystore network id %d: a devnet needs testnet (0)", faucet.Address().NetworkId())
	}
	staked := make([]stakedPool, s.Pools)
	for i := range staked {
		pk, err := k.Pool(keys.ActorID(i), 0)
		if err != nil {
			return Bundle{}, fmt.Errorf("pool %d keys: %w", i, err)
		}
		staked[i] = stakedPool{keys: pk, owner: k.Wallet(keys.ActorID(i))}
	}

	b := Bundle{Faucet: faucet.Address(), SpecHash: specHash(s)}
	var err error
	if b.Alonzo, err = renderAlonzo(s); err != nil {
		return Bundle{}, fmt.Errorf("alonzo genesis: %w", err)
	}
	if b.Byron, err = renderByron(s); err != nil {
		return Bundle{}, fmt.Errorf("byron genesis: %w", err)
	}
	if b.Shelley, err = renderShelley(s, faucet, staked); err != nil {
		return Bundle{}, fmt.Errorf("shelley genesis: %w", err)
	}
	if b.Conway, err = renderConway(s, k); err != nil {
		return Bundle{}, fmt.Errorf("conway genesis: %w", err)
	}
	if b.Hashes.Byron, err = ByronHash(b.Byron); err != nil {
		return Bundle{}, err
	}
	b.Hashes.Shelley, b.Hashes.Alonzo, b.Hashes.Conway = fileHash(b.Shelley), fileHash(b.Alonzo), fileHash(b.Conway)
	if b.NodeConfig, err = renderNodeConfig(b.Hashes); err != nil {
		return Bundle{}, fmt.Errorf("node config: %w", err)
	}
	if b.Pools, err = renderPools(s, k, staked); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// fileHash is how cardano-node hashes the Shelley, Alonzo and Conway
// genesis files: Blake2b-256 of the bytes as read.
func fileHash(b []byte) string {
	h := common.Blake2b256Hash(b)
	return hex.EncodeToString(h[:])
}

func renderPools(s Spec, k *keys.Store, staked []stakedPool) ([]Pool, error) {
	out := make([]Pool, len(staked))
	for i, sp := range staked {
		files, err := poolFiles(k, keys.ActorID(i), sp.keys)
		if err != nil {
			return nil, fmt.Errorf("pool %d files: %w", i, err)
		}
		topo, err := renderTopology(s, i)
		if err != nil {
			return nil, fmt.Errorf("pool %d topology: %w", i, err)
		}
		out[i] = Pool{ID: sp.keys.PoolID, Topology: topo, Files: files}
	}
	return out, nil
}

// poolFiles issues the opcert at KES period 0 with counter 0: a fresh
// network starts at period 0 and no earlier cert exists.
func poolFiles(k *keys.Store, id keys.ActorID, pk keys.PoolKeys) (map[string][]byte, error) {
	kes, err := pk.KESSKey.TextEnvelope()
	if err != nil {
		return nil, fmt.Errorf("kes.skey: %w", err)
	}
	vrf, err := pk.VRFSKey.TextEnvelope()
	if err != nil {
		return nil, fmt.Errorf("vrf.skey: %w", err)
	}
	oc, err := k.OpCert(id, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	opcert, err := envelope("NodeOperationalCertificate", "", oc)
	if err != nil {
		return nil, err
	}
	// 0x5820 is the CBOR header of a 32-byte string.
	cold, err := envelope("StakePoolVerificationKey_ed25519", "Stake Pool Operator Verification Key",
		append([]byte{0x58, 0x20}, pk.ColdVKey[:]...))
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"kes.skey": kes, "vrf.skey": vrf, "opcert.cert": opcert, "cold.vkey": cold}, nil
}

func envelope(typ, desc string, cbor []byte) ([]byte, error) {
	b, err := json.Marshal(struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		CborHex     string `json:"cborHex"`
	}{typ, desc, hex.EncodeToString(cbor)})
	if err != nil {
		return nil, fmt.Errorf("envelope %s: %w", typ, err)
	}
	return b, nil
}
