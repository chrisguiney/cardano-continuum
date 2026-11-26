//go:build smoke

package forger

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

const (
	smokePrefix  = "forger-"
	smokeNetwork = smokePrefix + "net"
	// smokeWindow is each misbehaviour's turn, in slots. The forger leads
	// about one slot in eleven, so a turn has a leader slot 98% of the time.
	smokeWindow = 40
)

// smokeNode is one container of the devnet.
type smokeNode struct {
	Name, Impl, Image string
	// IP is its address on the devnet network, which is how the forger
	// names it as a peer.
	IP string
}

// TestSmokeMisbehaviours runs one cardano-node pool, one dingo pool and the
// forger as the third pool on a rendered devnet in Docker, gives every
// misbehaviour a turn, and records what each honest node did with each
// forged block. It fails if an honest node holds a block that had to be
// rejected, crashed, or forked from the other past k. It needs Docker and
// the forger image, and takes about ten minutes:
//
//	make ko-images CONTINUUM_REGISTRY=localhost:5005/continuum
//	CONTINUUM_SMOKE_FORGER_IMAGE=localhost:5005/continuum/continuum-forger \
//	  go test -tags smoke -run TestSmokeMisbehaviours -v -timeout 25m ./internal/forger
//
// CONTINUUM_SMOKE_OUT names a directory that receives every container's
// log, the forged blocks and report.json.
func TestSmokeMisbehaviours(t *testing.T) {
	forgerImage := os.Getenv("CONTINUUM_SMOKE_FORGER_IMAGE")
	if forgerImage == "" {
		t.Fatal("CONTINUUM_SMOKE_FORGER_IMAGE is not set")
	}
	out := os.Getenv("CONTINUUM_SMOKE_OUT")
	if out == "" {
		out = t.TempDir()
	}
	nodes := []smokeNode{
		{Name: smokePrefix + "pool-0", Impl: "cardano-node", Image: envOr("CONTINUUM_SMOKE_NODE_IMAGE", "ghcr.io/blinklabs-io/cardano-node:11.1.3-2")},
		{Name: smokePrefix + "pool-1", Impl: "dingo", Image: envOr("CONTINUUM_SMOKE_DINGO_IMAGE", "ghcr.io/blinklabs-io/dingo:main")},
	}
	rogue := smokeNode{Name: smokePrefix + "pool-2", Impl: "rogue", Image: forgerImage}
	all := append(slices.Clone(nodes), rogue)

	s := genesis.Default()
	s.SecurityParam = 10
	s.ActiveSlotsCoeff = 0.25 // k/f = 40, so 200-slot epochs pass Check
	s.EpochLength = 200
	s.PoolHost = smokePrefix + "pool-%d"
	s.SystemStart = time.Now().Add(60 * time.Second).Truncate(time.Second).UTC()
	store, err := keys.New(rng.Seed{0x49}, 0)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := genesis.Render(s, store)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, bundle); err != nil {
		t.Fatal(err)
	}
	sched := smokeSchedule()
	writeRogueKeys(t, store, filepath.Join(dir, "pools", "2"), sched)
	evidence := filepath.Join(out, "evidence")
	if err := os.MkdirAll(evidence, 0o777); err != nil {
		t.Fatal(err)
	}
	startDevnet(t, dir, out, evidence, s.Magic, all)
	for i := range nodes {
		nodes[i].IP = strings.TrimSpace(dockerOut(t, "inspect", "-f",
			`{{(index .NetworkSettings.Networks "`+smokeNetwork+`").IPAddress}}`, nodes[i].Name))
	}

	// One turn for every rule, and slack for the last one's blocks to be
	// served and refused.
	turns := time.Duration(len(sched.Rules)*smokeWindow+40) * s.SlotLength
	deadline := s.SystemStart.Add(turns)
	t.Logf("system start %s; observing until %s", s.SystemStart.Format(time.TimeOnly), deadline.Format(time.TimeOnly))
	for time.Now().Before(deadline) {
		time.Sleep(min(15*time.Second, time.Until(deadline)+time.Second))
		for _, n := range all {
			if st := containerState(n.Name); !strings.HasPrefix(st, "running 0") {
				t.Errorf("%s (%s) is %q: it stopped or restarted during the run", n.Name, n.Impl, st)
				deadline = time.Now()
			}
		}
	}

	rep := smokeReport{Seed: hex.EncodeToString(sched.Seed[:]), K: uint64(s.SecurityParam), States: map[string]string{}}
	rep.read(t, dockerOut(t, "logs", rogue.Name), evidence)
	chains := map[string][]chain.Point{}
	for _, n := range nodes {
		rep.States[n.Name] = containerState(n.Name)
		rep.Images = append(rep.Images, n.Impl+" "+imageID(t, n.Image))
		port := strings.TrimSpace(dockerOut(t, "port", n.Name, "3001/tcp"))
		pts, by, err := fetchChain(port, s.Magic, bundle)
		if err != nil {
			t.Errorf("read %s's chain: %v", n.Name, err)
		}
		chains[n.Name] = pts
		rep.Chains = append(rep.Chains, fmt.Sprintf("%s (%s): %d blocks, tip slot %d, by pool %v", n.Name, n.Impl, len(pts), tipSlot(pts), by))
	}
	rep.Images = append(rep.Images, "rogue "+imageID(t, rogue.Image))
	rep.judge(nodes, chains)
	rep.check(t, nodes, chains)
	rep.write(t, out, nodes)
}

// startDevnet runs cardano-node as pool 0, dingo as pool 1 and, when all
// names a third node, the forger as pool 2 on their own Docker network, and
// removes them, with their logs saved to out, when the test ends.
func startDevnet(t *testing.T, dir, out, evidence string, magic uint32, all []smokeNode) {
	t.Helper()
	docker(t, "network", "create", smokeNetwork)
	t.Cleanup(func() { _, _ = exec.Command("docker", "network", "rm", smokeNetwork).CombinedOutput() })
	for _, n := range all {
		t.Cleanup(func() {
			saveLogs(t, out, n.Name)
			_, _ = exec.Command("docker", "rm", "-f", n.Name).CombinedOutput()
		})
	}
	cfg, pools := filepath.Join(dir, "config"), filepath.Join(dir, "pools")
	base := func(i int, extra ...string) []string {
		args := []string{"run", "-d", "--name", all[i].Name, "--hostname", all[i].Name, "--network", smokeNetwork,
			"-v", cfg + ":/config:ro", "-v", filepath.Join(pools, fmt.Sprint(i)) + ":/keys:ro"}
		return append(args, extra...)
	}
	docker(t, base(0, "-p", "127.0.0.1::3001", "--entrypoint", "cardano-node", all[0].Image, "run",
		"--config", "/config/config.json", "--topology", "/keys/topology.json",
		"--database-path", "/data/db", "--socket-path", "/data/node.socket",
		"--host-addr", "0.0.0.0", "--port", "3001",
		"--shelley-kes-key", "/keys/kes.skey", "--shelley-vrf-key", "/keys/vrf.skey",
		"--shelley-operational-certificate", "/keys/opcert.cert")...)
	writeDingoConfig(t, filepath.Join(pools, "1"), magic)
	// dingo's keystore refuses a key file its user does not own outright.
	docker(t, base(1, "-p", "127.0.0.1::3001", "--user", "0:0", "-e", "CARDANO_NETWORK=devnet",
		"-e", "CARDANO_DATABASE_PATH=/data/db", "-e", "CARDANO_SOCKET_PATH=/tmp/node.socket",
		"--entrypoint", "dingo", all[1].Image, "--config", "/keys/dingo.yaml", "serve")...)
	if len(all) < 3 {
		return
	}
	docker(t, base(2, "--user", "0:0", "-v", evidence+":/evidence",
		all[2].Image, "run", "--genesis", "/config", "--keys", "/keys", "--evidence", "/evidence",
		"--peer", all[0].Name+":3001", "--peer", all[1].Name+":3001")...)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// smokeSchedule gives each misbehaviour a turn. kes-expired is left out: a
// certificate cannot expire before KES period 60, some 90 days into this
// devnet.
func smokeSchedule() Schedule {
	s := Schedule{Seed: rng.Seed{0x49, 0x01}, Window: smokeWindow}
	for _, k := range Kinds() {
		switch k {
		case KESExpired:
		case VRFNotLeader, FutureSlot:
			// These fire in any slot; a low rate keeps a turn to a few.
			s.Rules = append(s.Rules, Rule{Kind: k, Rate: 0.1})
		default:
			s.Rules = append(s.Rules, Rule{Kind: k, Rate: 1})
		}
	}
	return s
}

// writeRogueKeys reissues pool 2's certificate with counter 1, so that a
// counter of 0 is a rewind once it has forged, and adds the alternate
// certificates and the schedule.
func writeRogueKeys(t *testing.T, store *keys.Store, dir string, sched Schedule) {
	t.Helper()
	for name, c := range map[string][2]uint64{
		CertFile: {1, 0}, certFile(CertRewind): {0, 0}, certFile(CertLeap): {7, 0}, certFile(CertFuture): {1, 50},
	} {
		raw, err := store.OpCert(2, 0, c[0], c[1])
		if err != nil {
			t.Fatal(err)
		}
		env := `{"type":"NodeOperationalCertificate","description":"","cborHex":"` + hex.EncodeToString(raw) + `"}`
		if err := os.WriteFile(filepath.Join(dir, name), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := json.Marshal(sched)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BehaviourFile), doc, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeDingoConfig is the block-producer config internal/operator's Dingo
// driver renders, for a container.
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

func imageID(t *testing.T, image string) string {
	t.Helper()
	return image + " " + strings.TrimSpace(dockerOut(t, "inspect", "-f",
		`{{.Id}} revision={{index .Config.Labels "org.opencontainers.image.revision"}}`, image))
}

func saveLogs(t *testing.T, dir, name string) {
	t.Helper()
	out, err := exec.Command("docker", "logs", name).CombinedOutput()
	if err != nil {
		t.Logf("logs %s: %v", name, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name+".log"), out, 0o644); err != nil {
		t.Logf("save %s logs: %v", name, err)
	}
}

func tipSlot(pts []chain.Point) uint64 {
	if len(pts) == 0 {
		return 0
	}
	return pts[len(pts)-1].Slot
}

// fetchChain reads a node's whole selected chain over node-to-node
// chain-sync, and counts its blocks by genesis pool index.
func fetchChain(addr string, magic uint32, b genesis.Bundle) ([]chain.Point, map[int]int, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	var pts []chain.Point
	by := map[int]int{}
	done := make(chan struct{})
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn), ouroboros.WithNetworkMagic(magic), ouroboros.WithNodeToNode(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollForwardFunc(func(_ chainsync.CallbackContext, _ uint, h any, tip chainsync.Tip) error {
				hd := h.(ledger.BlockHeader)
				pts = append(pts, chain.Point{Slot: hd.SlotNumber(), Hash: hd.Hash()})
				issuer := hd.IssuerVkey()
				pool := common.Blake2b224Hash(issuer[:])
				by[slices.IndexFunc(b.Pools, func(p genesis.Pool) bool { return p.ID == [28]byte(pool) })]++
				if bytes.Equal(tip.Point.Hash, hd.Hash().Bytes()) {
					close(done)
				}
				return nil
			}),
			chainsync.WithRollBackwardFunc(func(chainsync.CallbackContext, pcommon.Point, chainsync.Tip) error { return nil }),
		)),
	)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	defer func() { _ = oc.Close() }()
	if err := oc.ChainSync().Client.Sync(nil); err != nil {
		return nil, nil, err
	}
	select {
	case <-done:
		return pts, by, nil
	case err := <-oc.ErrorChan():
		return pts, by, fmt.Errorf("connection ended at %d blocks: %v", len(pts), err)
	case <-time.After(60 * time.Second):
		return pts, by, fmt.Errorf("tip not reached in 60s, at %d blocks", len(pts))
	}
}

// smokeBlock is one forged block and what happened to it.
type smokeBlock struct {
	Kind, Hash, Expect, Rule string
	Slot, BlockSlot          uint64
	SendTo                   []string
	Evidence                 string
	// Reaction is each honest node's, by container name.
	Reaction map[string]string
	cbor     []byte
	at       time.Time
}

type smokeEvent struct {
	at         time.Time
	msg, peer  string
	hash, text string
}

// smokeReport is what the run recorded.
type smokeReport struct {
	Seed     string
	K        uint64
	Images   []string
	States   map[string]string
	Chains   []string
	Blocks   []*smokeBlock
	Findings []string
	events   []smokeEvent
}

// read parses the forger's JSON log: its forged records, and the serve and
// disconnect events the reactions are read from.
func (r *smokeReport) read(t *testing.T, log, evidence string) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		str := func(k string) string { s, _ := rec[k].(string); return s }
		num := func(k string) uint64 { f, _ := rec[k].(float64); return uint64(f) }
		at, _ := time.Parse(time.RFC3339Nano, str("time"))
		switch str("msg") {
		case "forged":
			b := &smokeBlock{Kind: str("kind"), Hash: str("hash"), Expect: str("expect"), Rule: str("rule"),
				Slot: num("slot"), BlockSlot: num("block_slot"), Reaction: map[string]string{}, at: at}
			for _, p := range rec["send_to"].([]any) {
				b.SendTo = append(b.SendTo, p.(string))
			}
			b.Evidence = filepath.Join(evidence, filepath.Base(str("evidence")))
			var err error
			if b.cbor, err = os.ReadFile(b.Evidence); err != nil {
				t.Errorf("forged block %s has no evidence file: %v", b.Hash, err)
			}
			r.Blocks = append(r.Blocks, b)
		case "served invalid header", "served invalid block body", "peer disconnected", "upstream adopted an invalid block":
			peer := str("peer")
			if peer == "" {
				peer, _, _ = net.SplitHostPort(str("remote"))
			}
			r.events = append(r.events, smokeEvent{at: at, msg: str("msg"), peer: peer, hash: str("hash"), text: str("error")})
		}
	}
}

// judge fills in each node's reaction to each forged block: adopted when
// the block is on its chain; otherwise what the forger saw of it.
func (r *smokeReport) judge(nodes []smokeNode, chains map[string][]chain.Point) {
	for _, b := range r.Blocks {
		for _, n := range nodes {
			b.Reaction[n.Name] = r.reaction(b, n, chains[n.Name])
		}
	}
}

func (r *smokeReport) reaction(b *smokeBlock, n smokeNode, pts []chain.Point) string {
	if slices.ContainsFunc(pts, func(p chain.Point) bool { return hex.EncodeToString(p.Hash[:]) == b.Hash }) {
		return "adopted"
	}
	if b.Expect == string(Adopt) {
		return "not selected"
	}
	var header, body bool
	var served time.Time
	for _, e := range r.events {
		if e.hash != b.Hash || e.peer != n.IP {
			continue
		}
		if e.msg == "served invalid header" {
			header, served = true, e.at
		}
		body = body || e.msg == "served invalid block body"
	}
	if !header {
		return "not offered"
	}
	got := "header served"
	if body {
		got = "header and body served"
	}
	for _, e := range r.events {
		if e.msg == "peer disconnected" && e.peer == n.IP && !e.at.Before(served) && e.at.Sub(served) < 15*time.Second {
			return got + "; rejected, disconnected after " + e.at.Sub(served).Round(time.Millisecond).String()
		}
	}
	return got + "; rejected, stayed connected"
}

// check applies the oracle: no rogue block on an honest chain, tips within
// k; and the forger's own sanity: honest blocks it forged were adopted.
func (r *smokeReport) check(t *testing.T, nodes []smokeNode, chains map[string][]chain.Point) {
	t.Helper()
	snap := oracle.Snapshot{}
	for _, b := range r.Blocks {
		if b.Expect != string(Reject) {
			continue
		}
		raw, _ := hex.DecodeString(b.Hash)
		snap.Rogue = append(snap.Rogue, oracle.RogueBlock{
			Point: chain.Point{Slot: b.BlockSlot, Hash: common.Blake2b256(raw)}, Kind: b.Kind, Rule: b.Rule, CBOR: b.cbor,
		})
	}
	for i, n := range nodes {
		snap.Tips = append(snap.Tips, oracle.Branch{
			Node: oracle.Node{Impl: n.Impl, Pool: n.Name, Digest: r.Images[i]}, Points: chains[n.Name],
		})
	}
	for _, f := range oracle.Check(snap, oracle.Monitors(r.K, policy.Guard{})) {
		line := fmt.Sprintf("%s %s: %s; nodes %+v; block cbor %d bytes", f.Severity, f.Kind, f.Summary, f.Nodes, len(f.Block))
		r.Findings = append(r.Findings, line)
		t.Errorf("FINDING %s", line)
	}
	honest, adopted := 0, 0
	for _, b := range r.Blocks {
		if b.Kind != string(Honest) {
			continue
		}
		honest++
		if b.Reaction[nodes[0].Name] == "adopted" {
			adopted++
		}
	}
	if adopted == 0 {
		t.Errorf("none of the forger's %d honest blocks is on %s's chain: it does not work as a pool, so the rejections say nothing", honest, nodes[0].Name)
	}
	for _, e := range r.events {
		if e.msg == "upstream adopted an invalid block" {
			t.Errorf("forger saw its upstream adopt invalid block %s", e.hash)
		}
	}
}

func (r *smokeReport) write(t *testing.T, out string, nodes []smokeNode) {
	t.Helper()
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "report.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report and logs in %s", out)
	for _, line := range r.table(nodes) {
		t.Log(line)
	}
}

// table is one line per misbehaviour and node: how many blocks, and the
// reactions with their counts.
func (r *smokeReport) table(nodes []smokeNode) []string {
	count := map[string]map[string]int{}
	total := map[string]int{}
	for _, b := range r.Blocks {
		total[b.Kind]++
		for _, n := range nodes {
			key := b.Kind + " | " + n.Impl
			if count[key] == nil {
				count[key] = map[string]int{}
			}
			// The disconnect delay differs per block; the table groups
			// without it.
			reaction, _, _ := strings.Cut(b.Reaction[n.Name], " after ")
			count[key][reaction]++
		}
	}
	var out []string
	for key, m := range count {
		var parts []string
		for reaction, n := range m {
			parts = append(parts, fmt.Sprintf("%d %s", n, reaction))
		}
		sort.Strings(parts)
		kind, _, _ := strings.Cut(key, " | ")
		out = append(out, fmt.Sprintf("%-34s (%d forged): %s", key, total[kind], strings.Join(parts, "; ")))
	}
	sort.Strings(out)
	return append(append(out, r.Chains...), r.Images...)
}
