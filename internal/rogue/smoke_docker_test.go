//go:build smoke

package rogue

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// clearedGenesis builds a devnet clearance for magic. The smoke run controls
// both sides of the Guard, so a self-consistent non-public genesis clears.
func clearedGenesis(magic uint32) adversary.Clearance {
	g := chain.Genesis{Magic: magic, Start: time.Unix(0, 0).UTC(), EpochLength: 600, SecurityParam: 5, SlotLength: time.Second}
	c, _ := adversary.Clear(g, g)
	return c
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// startNodes runs cardano-node as pool 0 and dingo as pool 1 on their own
// Docker network, publishing each node-to-node port to the host, and removes
// them with their logs saved to out when the test ends.
func startNodes(t *testing.T, dir, out string, magic uint32, all []*smokeNode) {
	t.Helper()
	docker(t, "network", "create", smokeNetwork)
	t.Cleanup(func() { _, _ = exec.Command("docker", "network", "rm", smokeNetwork).CombinedOutput() })
	for _, n := range all {
		name := n.Name
		t.Cleanup(func() {
			if logs, err := exec.Command("docker", "logs", name).CombinedOutput(); err == nil {
				_ = os.WriteFile(filepath.Join(out, name+".log"), logs, 0o644)
			}
			_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
		})
	}
	cfg, pools := filepath.Join(dir, "config"), filepath.Join(dir, "pools")
	base := func(i int, extra ...string) []string {
		args := []string{"run", "-d", "--name", all[i].Name, "--hostname", all[i].Name, "--network", smokeNetwork,
			"-v", cfg + ":/config:ro", "-v", filepath.Join(pools, fmt.Sprint(i)) + ":/keys:ro", "-p", "127.0.0.1::3001"}
		return append(args, extra...)
	}
	docker(t, base(0, "--entrypoint", "cardano-node", all[0].Image, "run",
		"--config", "/config/config.json", "--topology", "/keys/topology.json",
		"--database-path", "/data/db", "--socket-path", "/data/node.socket",
		"--host-addr", "0.0.0.0", "--port", "3001",
		"--shelley-kes-key", "/keys/kes.skey", "--shelley-vrf-key", "/keys/vrf.skey",
		"--shelley-operational-certificate", "/keys/opcert.cert")...)
	writeDingoConfig(t, filepath.Join(pools, "1"), magic)
	docker(t, base(1, "--user", "0:0", "-e", "CARDANO_NETWORK=devnet",
		"-e", "CARDANO_DATABASE_PATH=/data/db", "-e", "CARDANO_SOCKET_PATH=/tmp/node.socket",
		"--entrypoint", "dingo", all[1].Image, "--config", "/keys/dingo.yaml", "serve")...)
}

func writeDingoConfig(t *testing.T, dir string, magic uint32) {
	t.Helper()
	cfg := fmt.Sprintf(`network: devnet
networkMagic: %d
cardanoConfig: /config/config.json
topology: /keys/topology.json
databasePath: /data/db
socketPath: /tmp/node.socket
bindAddr: 0.0.0.0
relayPort: 3001
privateBindAddr: 127.0.0.1
privatePort: 3002
blockProducer: true
shelleyVrfKey: /keys/vrf.skey
shelleyKesKey: /keys/kes.skey
shelleyOperationalCertificate: /keys/opcert.cert
`, magic)
	if err := os.WriteFile(filepath.Join(dir, "dingo.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// publishedPort returns the host address a node's 3001 is reachable at.
func publishedPort(t *testing.T, name string) string {
	t.Helper()
	out := strings.TrimSpace(dockerOut(t, "port", name, "3001/tcp"))
	// docker prints "127.0.0.1:NNNNN" (one or more lines); take the first.
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	if out == "" {
		t.Fatalf("no published port for %s", name)
	}
	return out
}

// waitReady blocks until addr accepts a TCP connection or ctx ends, so the
// node's node-to-node listener is up before any rogue connects. It does not
// require a completed handshake: a node may refuse our handshake version, which
// is itself a reaction to record, not a reason to skip the run.
func waitReady(t *testing.T, ctx context.Context, addr string, magic uint32) {
	t.Helper()
	for {
		if tcpReachable(addr) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("node at %s not reachable before deadline", addr)
		case <-time.After(3 * time.Second):
		}
	}
}

// tcpReachable reports whether addr accepts a TCP connection. A node that has
// crashed or whose accept loop is stuck fails it.
func tcpReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
}

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
	return string(out)
}

func containerState(name string) string {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{.RestartCount}} exit={{.State.ExitCode}}", name).CombinedOutput()
	if err != nil {
		return "gone: " + strings.TrimSpace(string(out))
	}
	return strings.TrimSpace(string(out))
}
