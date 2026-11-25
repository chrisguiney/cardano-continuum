//go:build devnet

package submit

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// TestDevnetLocalSubmit boots one cardano-node on a rendered devnet and
// submits over its local-tx-submission: a valid payment from the funded
// faucet is accepted, and a bad one is refused with its ledger class. It
// needs Docker and takes about two minutes:
//
//	go test -tags devnet -run TestDevnetLocalSubmit -v -timeout 10m ./internal/submit
//
// CONTINUUM_SMOKE_NODE_IMAGE overrides the node image.
func TestDevnetLocalSubmit(t *testing.T) {
	image := os.Getenv("CONTINUUM_SMOKE_NODE_IMAGE")
	if image == "" {
		image = "ghcr.io/blinklabs-io/cardano-node:11.1.3-1"
	}
	const (
		prefix = "continuum-submit-"
		name   = prefix + "node"
		sock   = "/data/node.socket"
	)
	s := genesis.Default()
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25 // k/f = 40, so 200-slot epochs pass Check
	s.EpochLength = 200
	s.PoolHost = prefix + "pool-%d"
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
			out, _ := exec.Command("docker", "logs", "--tail", "30", name).CombinedOutput()
			t.Logf("node logs:\n%s", out)
		}
		_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
	})
	docker(t, "run", "-d", "--name", name, "--hostname", name, "-p", "127.0.0.1::3333",
		"-v", filepath.Join(dir, "config")+":/config:ro",
		"-v", filepath.Join(dir, "pools", "0")+":/keys:ro",
		"--entrypoint", "cardano-node", image, "run",
		"--config", "/config/config.json", "--topology", "/keys/topology.json",
		"--database-path", "/data/db", "--socket-path", sock,
		"--host-addr", "0.0.0.0", "--port", strconv.Itoa(s.PoolPort),
		"--shelley-kes-key", "/keys/kes.skey", "--shelley-vrf-key", "/keys/vrf.skey",
		"--shelley-operational-certificate", "/keys/opcert.cert")
	waitTip(t, name, s.Magic, s.SystemStart.Add(3*time.Minute))
	// socat bridges the node's unix socket to a TCP port the host can dial.
	docker(t, "exec", "-d", name, "socat", "TCP-LISTEN:3333,fork,reuseaddr", "UNIX-CONNECT:"+sock)
	addr := hostPort(t, name)

	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
	l := NewLocal(dial, s.Magic)
	faucet := st.Faucet()
	total := s.FaucetLovelace
	good := spec{dest: st.Wallet(1).Address(), pay: 10_000_000, fee: okFee, signers: []keys.Wallet{faucet}}
	const era = 6
	send := func(sp spec) Outcome {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return Send(ctx, l, era, encodeTx(t, faucet, total, sp))
	}

	cheap := good
	cheap.fee = 1
	o := send(cheap)
	t.Logf("fee 1: %+v", o)
	if o.Verdict != Rejected || !o.Has(FeeTooSmall) {
		t.Fatalf("fee 1: %+v, want rejected with %s", o, FeeTooSmall)
	}
	ghost := good
	ghost.extraIn = true
	o = send(ghost)
	t.Logf("unknown input: %+v", o)
	if o.Verdict != Rejected || !o.Has(BadInputs) {
		t.Fatalf("unknown input: %+v, want rejected with %s", o, BadInputs)
	}
	o = send(good)
	t.Logf("valid payment: %+v", o)
	if o.Verdict != Accepted {
		t.Fatalf("valid payment: %+v", o)
	}
	o = send(good)
	t.Logf("resubmit: %+v", o)
	if o.Verdict != Rejected || !o.Has(MempoolFailure) {
		t.Fatalf("resubmit: %+v, want rejected with %s", o, MempoolFailure)
	}
}

func waitTip(t *testing.T, name string, magic uint32, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		err := exec.Command("docker", "exec", name, "cardano-cli", "query", "tip",
			"--testnet-magic", strconv.Itoa(int(magic)), "--socket-path", "/data/node.socket").Run()
		if err == nil {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("node %s never answered a tip query", name)
}

func hostPort(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "port", name, "3333/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, fmt.Sprint(string(out)))
	}
}
