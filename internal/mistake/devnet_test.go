//go:build devnet

package mistake

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

const (
	devPrefix = "continuum-mistake-"
	devName   = devPrefix + "node"
	devSock   = "/data/node.socket"
	conwayEra = 6
)

// TestDevnetCatalog runs the mistake catalog against one cardano-node on a
// rendered devnet (k=10, f=0.25, 200-slot epochs) over local-tx-submission.
// It needs Docker and takes a few minutes:
//
//	go test -tags devnet -run TestDevnetCatalog -v -timeout 15m ./internal/mistake
//
// CONTINUUM_SMOKE_NODE_IMAGE overrides the node image.
func TestDevnetCatalog(t *testing.T) {
	image := os.Getenv("CONTINUUM_SMOKE_NODE_IMAGE")
	if image == "" {
		image = "ghcr.io/blinklabs-io/cardano-node:11.1.3-1"
	}
	s := genesis.Default()
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25
	s.EpochLength = 200
	s.PoolHost = devPrefix + "pool-%d"
	s.SystemStart = time.Now().Add(20 * time.Second).Truncate(time.Second).UTC()
	st, err := keys.New(rng.Seed{7}, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := genesis.Render(s, st)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "30", devName).CombinedOutput()
			t.Logf("node logs:\n%s", out)
		}
		_, _ = exec.Command("docker", "rm", "-f", devName).CombinedOutput()
	})
	docker(t, "run", "-d", "--name", devName, "--hostname", devName, "-p", "127.0.0.1::3333",
		"-v", filepath.Join(dir, "config")+":/config:ro",
		"-v", filepath.Join(dir, "pools", "0")+":/keys:ro",
		"--entrypoint", "cardano-node", image, "run",
		"--config", "/config/config.json", "--topology", "/keys/topology.json",
		"--database-path", "/data/db", "--socket-path", devSock,
		"--host-addr", "0.0.0.0", "--port", strconv.Itoa(s.PoolPort),
		"--shelley-kes-key", "/keys/kes.skey", "--shelley-vrf-key", "/keys/vrf.skey",
		"--shelley-operational-certificate", "/keys/opcert.cert")
	n := &node{magic: s.Magic}
	n.waitTip(t, s.SystemStart.Add(3*time.Minute))
	docker(t, "exec", "-d", devName, "socat", "TCP-LISTEN:3333,fork,reuseaddr", "UNIX-CONNECT:"+devSock)
	addr := hostPort(t)
	n.target = submit.NewLocal(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, s.Magic)

	p := n.params(t)
	p.SystemStart, p.SlotLength = s.SystemStart, s.SlotLength
	faucet := st.Faucet()
	wd := &world{
		c: n, b: build.New(p), p: p, w: faucet, dest: st.Wallet(1).Address(),
		script: redeemerScript(t), funds: []common.Utxo{genesisUtxo(t, faucet.Address(), s.FaucetLovelace)},
		ahead: 60,
	}
	for _, r := range runCatalog(t, wd) {
		t.Log(r)
	}
}

type node struct {
	magic  uint32
	target submit.Target
}

func (n *node) cli(t *testing.T, args ...string) []byte {
	t.Helper()
	args = append([]string{"exec", devName, "cardano-cli", "conway"}, args...)
	args = append(args, "--testnet-magic", strconv.Itoa(int(n.magic)), "--socket-path", devSock)
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("cardano-cli %s: %v", args[4], err)
	}
	return out
}

func (n *node) waitTip(t *testing.T, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		err := exec.Command("docker", "exec", devName, "cardano-cli", "query", "tip",
			"--testnet-magic", strconv.Itoa(int(n.magic)), "--socket-path", devSock).Run()
		if err == nil {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("node %s never answered a tip query", devName)
}

func (n *node) send(t *testing.T, tx []byte) submit.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return submit.Send(ctx, n.target, conwayEra, tx)
}

func (n *node) slot(t *testing.T) uint64 {
	t.Helper()
	var tip struct{ Slot uint64 }
	if err := json.Unmarshal(n.cli(t, "query", "tip"), &tip); err != nil {
		t.Fatal(err)
	}
	return tip.Slot
}

// land polls until output 0 of id is unspent, which needs id in a block.
func (n *node) land(t *testing.T, id common.Blake2b256) {
	t.Helper()
	in := fmt.Sprintf("%x#0", id[:])
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		out := n.cli(t, "query", "utxo", "--tx-in", in, "--output-json")
		if strings.Contains(string(out), in) {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("tx %s never landed", in)
}

// params reads the node's protocol parameters into build.Params. Prices are
// decimal literals in the JSON, read exactly as rationals.
func (n *node) params(t *testing.T) build.Params {
	t.Helper()
	var pp struct {
		TxFeePerByte, TxFeeFixed, UtxoCostPerByte uint64
		MaxTxSize                                 uint32
		StakeAddressDeposit, StakePoolDeposit     uint64
		DRepDeposit, GovActionDeposit             uint64
		MinPoolCost, PoolRetireMaxEpoch           uint64
		CollateralPercentage, MaxCollateralInputs uint
		ExecutionUnitPrices                       struct{ PriceMemory, PriceSteps json.Number }
		MaxTxExecutionUnits                       struct{ Memory, Steps int64 }
		ProtocolVersion                           struct{ Major uint }
		CostModels                                struct{ PlutusV3 []int64 }
	}
	dec := json.NewDecoder(strings.NewReader(string(n.cli(t, "query", "protocol-parameters"))))
	dec.UseNumber()
	if err := dec.Decode(&pp); err != nil {
		t.Fatal(err)
	}
	rat := func(x json.Number) *cbor.Rat {
		r, ok := new(big.Rat).SetString(x.String())
		if !ok {
			t.Fatalf("price %q", x)
		}
		return &cbor.Rat{Rat: r}
	}
	return build.Params{
		MinFeeA: pp.TxFeePerByte, MinFeeB: pp.TxFeeFixed, CoinsPerUtxoByte: pp.UtxoCostPerByte,
		MaxTxSize: pp.MaxTxSize, KeyDeposit: pp.StakeAddressDeposit, PoolDeposit: pp.StakePoolDeposit,
		DRepDeposit: pp.DRepDeposit, GovActionDeposit: pp.GovActionDeposit, MinPoolCost: pp.MinPoolCost,
		MaxRetireEpochs: pp.PoolRetireMaxEpoch,
		Prices: common.ExUnitPrice{
			MemPrice: rat(pp.ExecutionUnitPrices.PriceMemory), StepPrice: rat(pp.ExecutionUnitPrices.PriceSteps),
		},
		MaxTxExUnits:      common.ExUnits{Memory: pp.MaxTxExecutionUnits.Memory, Steps: pp.MaxTxExecutionUnits.Steps},
		CollateralPercent: pp.CollateralPercentage, MaxCollateralInputs: pp.MaxCollateralInputs,
		Protocol: protocol.Version{Major: pp.ProtocolVersion.Major}, PlutusV3: pp.CostModels.PlutusV3,
	}
}

// genesisUtxo is the faucet's Shelley genesis output: its id is the
// Blake2b-256 of the address bytes, index 0.
func genesisUtxo(t *testing.T, a common.Address, lovelace uint64) common.Utxo {
	t.Helper()
	ab, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cbor.Encode(map[uint]any{0: ab, 1: lovelace})
	if err != nil {
		t.Fatal(err)
	}
	var out babbage.BabbageTransactionOutput
	if _, err := cbor.Decode(raw, &out); err != nil {
		t.Fatal(err)
	}
	return common.Utxo{Id: shelley.ShelleyTransactionInput{TxId: common.Blake2b256Hash(ab)}, Output: &out}
}

func hostPort(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "port", devName, "3333/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
}
