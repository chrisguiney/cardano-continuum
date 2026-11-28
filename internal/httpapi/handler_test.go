package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/people"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(body), "", "  "); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", name, err, body)
	}
	buf.WriteByte('\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("%s differs from golden:\n%s", name, buf.String())
	}
}

// fakeSource serves fixed data; its feed is a real Hub.
type fakeSource struct {
	hub     *Hub
	lastQ   ActorQuery
	timeErr error
}

func (f *fakeSource) Snapshot(context.Context) (Snapshot, error) {
	epoch := uint64(3)
	return Snapshot{
		LastSeq: 120, SimTimeMs: 61000, Tip: &Point{Slot: 77, Hash: "0707"}, Blocks: 5, Epoch: &epoch,
		TxPerSec: 0.5, SubmitPerSec: 0.75,
		Ada:        Ada{FeesLovelace: 350000, SubmittedLovelace: 10_000_000, ConfirmedLovelace: 5_000_000},
		Population: Population{Total: 2, ByProfile: map[string]int{"hodl": 1, "yield": 1}, ByState: map[string]int{"active": 2}},
		Txs:        TxCounts{Submitted: 2, Confirmed: 1, Retries: 1},
		Invariants: []Invariant{{Name: "log_gapless", OK: true}},
	}, nil
}

func (f *fakeSource) Actors(_ context.Context, q ActorQuery) (ActorPage, error) {
	f.lastQ = q
	return ActorPage{Total: 2, Offset: q.Offset, Limit: q.Limit, Rows: []ActorRow{
		{ID: 1, Name: "alice", Role: "wallet", Profile: "hodl", State: "active", Intents: 4, Txs: 2, Retries: 1, Confirmed: 1, Kind: "wallet", Address: "addr_test1qz…abcdef", Balance: 5_000_000, Pending: 1_000_000, Tokens: 1, Delegated: true, Pool: "pool1abc…xyz123", DRep: "always_abstain"},
		{ID: 2, Name: "bob", Role: "wallet", Profile: "yield", State: "active"},
	}}, nil
}

func (f *fakeSource) ActorTimeline(_ context.Context, id uint64) (Timeline, error) {
	if f.timeErr != nil {
		return Timeline{}, f.timeErr
	}
	if id != 1 {
		return Timeline{}, ErrNotFound
	}
	in := uint64(9)
	return Timeline{Actor: 1, Profile: "hodl", Role: "wallet", State: "active", BornMs: 0, Entries: []TimelineEntry{
		{Seq: 2, At: 0, Kind: "Spawned", Detail: "hodl"},
		{Seq: 6, At: 6000, Kind: "Submitted", Detail: "5000000 lovelace to 2", Intent: &in},
	}}, nil
}

func (f *fakeSource) ActorDetail(_ context.Context, id uint64) (ActorDetail, error) {
	if id != 1 {
		return ActorDetail{}, ErrNotFound
	}
	return ActorDetail{
		ID: 1, Name: "alice", Profile: "hodl", Kind: "wallet", Agenda: "hodl", HasIdentity: true,
		Wallet:     WalletIDs{PaymentAddr: "addr_test1qz", StakeAddr: "stake_test1uz", PaymentKeyHash: "aa", StakeKeyHash: "bb"},
		Holdings:   Holdings{SettledLovelace: 5_000_000, UTxOs: 2, Tokens: []store.Token{{Policy: "11", Name: "6162", NameText: "ab", Quantity: 3}}},
		Delegation: Delegation{StakeRegistered: true, Pool: "pool1abc"},
		Lifecycle:  Lifecycle{State: "active", ChangedMs: 5000, Hazard: 0.002},
		Traits:     Traits{Competence: 0.9},
		Activity:   Activity{Intents: 4, Submitted: 2, Confirmed: 1, Recent: []Decision{}},
		Findings:   []ActorFinding{}, Timeline: []TimelineEntry{{Seq: 2, Kind: "Spawned", Detail: "hodl"}},
	}, nil
}

func (f *fakeSource) Adversaries(context.Context) (AdversaryView, error) {
	ctl := 0.0
	delta := 1.0
	return AdversaryView{HasControl: true, Adversaries: []AdversaryRow{{
		ID: 3, Name: "adversary-malformed-cbor-3", Profile: "adversary-malformed-cbor", Attack: "malformed-cbor", Agenda: "profit", Budget: 100_000_000,
		Cost:      CostView{Spent: 4_000_000, Ceiling: 10_000_000, Epoch: 1, EpochSpent: 4_000_000, ByEpoch: []EpochCost{{Epoch: 1, Spent: 4_000_000}}},
		Metric:    SuccessView{Name: "fatal_findings", Value: 1, Control: &ctl},
		Attempted: 2, Landed: 0, Detected: 2, Rejected: 2,
		Recent:   []AttackStepRow{{Seq: 9, At: 3000, Intent: 70, Mutation: "bad-header", Cost: 2_000_000, Txs: 1}},
		Findings: []ActorFinding{{ID: "f", Seq: 5, Kind: "unexpected_accept", Severity: "fatal", Summary: "malformed accepted"}},
	}}, Impact: []ImpactRow{{Metric: "fatal_findings", Value: 1, Control: &ctl, Delta: &delta}}}, nil
}

func (f *fakeSource) Controls(context.Context) (ControlState, error) {
	return ControlState{Paused: true, K: 3, Weights: map[string]float64{"send": 2}, Last: &view.LastControl{RequestID: "ui-1", Op: "set_weights", Seq: 40, At: 9000}}, nil
}

func (f *fakeSource) Nodes(context.Context) (NodesView, error) {
	return NodesView{
		Nodes: []NodeRow{{Name: "p3", Label: "continuum-pool-3", Impl: "dingo", Tier: "forging", Phase: "Forging", Tip: Point{Slot: 100, Hash: "aa"},
			LastBlock: &Point{Slot: 95, Hash: "bb", Height: 19}, StakePool: strings.Repeat("0a", 28), Forged: new(uint64(4)), Verified: new(uint64(15)), Findings: 2, KnownFindings: 1}},
		Tips:  []ImplTip{{Impl: "dingo", Slot: 100, Hash: "aa", Nodes: 1}},
		Forks: []Fork{}, Topology: Topology{Mode: "mesh", Nodes: []Node{{ID: "p3", Impl: "dingo", Tier: "forging", Phase: "Forging"}}, Edges: []Edge{}},
	}, nil
}

func (f *fakeSource) StakePools(context.Context) (StakePoolsView, error) {
	actor := uint64(7)
	return StakePoolsView{Pools: []StakePoolRow{
		{ID: strings.Repeat("0a", 28), Bech32: view.PoolBech32(strings.Repeat("0a", 28)), Actor: &actor, ActorName: "pool-7", Profile: "pools",
			ActorState: "registered", Delegators: 2, DelegatedLovelace: 11_000_000, Votes: 1, Nodes: []view.NodeRef{}},
		{ID: strings.Repeat("0b", 28), Bech32: view.PoolBech32(strings.Repeat("0b", 28)), Nodes: []view.NodeRef{}},
	}}, nil
}

func (f *fakeSource) Governance(context.Context) (GovernanceView, error) {
	epoch, exp, actor := uint64(1), uint64(3), uint64(7)
	vote := VoteRow{Slot: 30, At: 30000, Tx: "20", Proposal: "10#0", Type: "info", Voter: "spo", VoterID: "07", Actor: &actor, Profile: "pools", Choice: "yes", Power: 5000}
	return GovernanceView{Epoch: &epoch, Lifetime: 2,
		Proposals: []ProposalRow{{ID: "10#0", Type: "info", Deposit: 100_000, Slot: 10, Epoch: &epoch, Expires: &exp, Status: "open", PoolsMayVote: true,
			Tally: map[string]Tally{"drep": {}, "spo": {Yes: 1, YesPower: 5000}, "committee": {}}}},
		Votes: []VoteRow{vote}, Pools: []PoolVotes{{Actor: 7, Profile: "pools", Votes: []VoteRow{vote}}},
	}, nil
}

func (f *fakeSource) Markets(context.Context) (view.MarketsView, error) {
	return view.MarketsView{Births: map[string]int{"EUR": 2}, Flows: []engine.MarketFlow{{Exchange: "north", Currency: "EUR", Side: "buy", Trades: 1}}}, nil
}

func (f *fakeSource) Proposal(context.Context, string) (ProposalDetail, error) {
	return ProposalDetail{}, ErrNotFound
}

func (f *fakeSource) Findings(_ context.Context, p Page) (FindingPage, error) {
	if p.ID == "f1" {
		return FindingPage{Rows: []Finding{{ID: "f1", Verdicts: []view.ImplVerdict{{Impl: "node", Verdict: "rejected Unknown", Detail: "Unknown: boom"}}}}}, nil
	}
	return FindingPage{Rows: []Finding{}}, nil
}

func (f *fakeSource) Subscribe(fl Filter, after *uint64) (*Subscription, error) {
	return f.hub.Subscribe(fl, after)
}

type fakeControls struct {
	got []Control
	err error
}

func (f *fakeControls) Submit(_ context.Context, c Control) (Receipt, error) {
	if f.err != nil {
		return Receipt{}, f.err
	}
	f.got = append(f.got, c)
	return Receipt{Seq: uint64(len(f.got))}, nil
}

func newServer(t *testing.T, ctl Controls) (*httptest.Server, *fakeSource) {
	t.Helper()
	src := &fakeSource{hub: NewHub(16, 2, 4)}
	h := NewHandler(Config{Source: src, Controls: ctl, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Heartbeat: time.Hour})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Cleanup(src.hub.Close)
	return srv, src
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestRESTGoldens(t *testing.T) {
	srv, _ := newServer(t, nil)
	for _, tt := range []struct{ name, path string }{
		{"overview", "/api/overview"},
		{"actors", "/api/actors?limit=2&sort=name&dir=desc&profile=hodl"},
		{"timeline", "/api/actors/1/timeline"},
		{"nodes", "/api/nodes"},
		{"pools", "/api/pools"},
		{"controls", "/api/controls"},
		{"findings", "/api/findings"},
	} {
		code, body := get(t, srv.URL+tt.path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", tt.path, code, body)
		}
		golden(t, tt.name, body)
	}
}

func TestActorsQueryParsedIntoSource(t *testing.T) {
	srv, src := newServer(t, nil)
	if code, body := get(t, srv.URL+"/api/actors?limit=7&offset=14&sort=state&dir=desc&profile=hodl&state=active&role=wallet"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	q := src.lastQ
	if q.Limit != 7 || q.Offset != 14 || q.Sort != "state" || !q.Desc ||
		q.Filter != (ActorFilter{Profile: "hodl", State: "active", Role: "wallet"}) {
		t.Fatalf("query %+v", q)
	}
	if code, _ := get(t, srv.URL+"/api/actors"); code != 200 {
		t.Fatal(code)
	}
	if src.lastQ.Limit != DefaultLimit || src.lastQ.Sort != "id" {
		t.Fatalf("defaults %+v", src.lastQ)
	}
}

func TestPagingAndSortValidation(t *testing.T) {
	srv, _ := newServer(t, nil)
	for _, q := range []string{
		"limit=0", "limit=501", "limit=-1", "limit=x", "offset=-1", "offset=x", "offset=2000000000",
		"sort=password", "sort=id;drop", "dir=sideways", "profile=" + strings.Repeat("a", 65), "state=A%20B",
	} {
		code, body := get(t, srv.URL+"/api/actors?"+q)
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, code, body)
		}
		var e struct{ Error string }
		if err := json.Unmarshal(body, &e); err != nil || e.Error == "" {
			t.Errorf("%s: no JSON error: %s", q, body)
		}
	}
	for _, q := range []string{"limit=1", "limit=500", "offset=0"} {
		if code, _ := get(t, srv.URL+"/api/actors?"+q); code != 200 {
			t.Errorf("%s: %d", q, code)
		}
	}
}

func TestTimelineErrors(t *testing.T) {
	srv, _ := newServer(t, nil)
	if code, _ := get(t, srv.URL+"/api/actors/99/timeline"); code != http.StatusNotFound {
		t.Errorf("unknown actor: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/actors/abc/timeline"); code != http.StatusBadRequest {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/findings/x"); code != http.StatusNotFound {
		t.Errorf("finding detail: %d", code)
	}
	if code, body := get(t, srv.URL+"/api/findings/f1"); code != 200 || !strings.Contains(string(body), "Unknown: boom") {
		t.Errorf("finding f1: %d %s", code, body)
	}
	if code, _ := get(t, srv.URL+"/healthz"); code != 200 {
		t.Errorf("healthz: %d", code)
	}
}

func post(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestControlPost(t *testing.T) {
	ctl := &fakeControls{}
	srv, _ := newServer(t, ctl)
	code, body := post(t, srv.URL+"/api/control", `{"request_id":"r1","op":"pause"}`)
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	golden(t, "control", body)
	if len(ctl.got) != 1 || ctl.got[0].Op != OpPause {
		t.Fatalf("got %+v", ctl.got)
	}
	for name, b := range map[string]string{
		"unknown field": `{"request_id":"r1","op":"pause","x":1}`,
		"bad op":        `{"request_id":"r1","op":"explode"}`,
		"not json":      `nope`,
	} {
		if code, _ := post(t, srv.URL+"/api/control", b); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	big := `{"request_id":"r1","op":"set_weights","weights":{"a":1},"pad":"` + strings.Repeat("x", 70<<10) + `"}`
	if code, _ := post(t, srv.URL+"/api/control", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", code)
	}
	if len(ctl.got) != 1 {
		t.Fatalf("rejected payloads reached the log: %+v", ctl.got)
	}
	if code, _ := get(t, srv.URL+"/api/control"); code != http.StatusMethodNotAllowed {
		t.Errorf("GET control: %d", code)
	}
}

func TestControlDuplicateIs200(t *testing.T) {
	srv, _ := newServer(t, dupControls{})
	if code, body := post(t, srv.URL+"/api/control", `{"request_id":"r1","op":"pause"}`); code != http.StatusOK || !strings.Contains(string(body), `"duplicate":true`) {
		t.Fatalf("%d %s", code, body)
	}
}

type dupControls struct{}

func (dupControls) Submit(context.Context, Control) (Receipt, error) {
	return Receipt{Seq: 4, Duplicate: true}, nil
}

func TestControlDisabledWithoutControls(t *testing.T) {
	srv, _ := newServer(t, nil)
	if code, _ := post(t, srv.URL+"/api/control", `{"request_id":"r1","op":"pause"}`); code != http.StatusNotImplemented {
		t.Fatalf("%d", code)
	}
}

func TestControlSubmitErrorIs500(t *testing.T) {
	srv, _ := newServer(t, &fakeControls{err: errors.New("disk full")})
	code, body := post(t, srv.URL+"/api/control", `{"request_id":"r1","op":"pause"}`)
	if code != http.StatusInternalServerError || strings.Contains(string(body), "disk full") {
		t.Fatalf("%d %s (internal errors must not leak)", code, body)
	}
}

type frame struct{ id, event, data string }

// readFrames reads n SSE frames, skipping comments, or fails at the deadline.
func readFrames(t *testing.T, r io.Reader, n int) []frame {
	t.Helper()
	sc := bufio.NewScanner(r)
	var out []frame
	var cur frame
	for len(out) < n && sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur != (frame{}) {
				out = append(out, cur)
			}
			cur = frame{}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
	if len(out) < n {
		t.Fatalf("got %d frames, want %d: %v", len(out), n, sc.Err())
	}
	return out
}

func openSSE(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestSSEDeliversFilteredInOrder(t *testing.T) {
	srv, src := newServer(t, nil)
	resp := openSSE(t, srv.URL+"/api/decisions?profile=hodl&kind=Submitted,Confirmed", nil)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	// The response header is flushed before the subscription goes live, so
	// wait for the hub to see the client before publishing.
	waitSubscribers(t, src.hub, 1)
	src.hub.Publish(Decision{ID: 1, Actor: 1, Profile: "hodl", Kind: "Spawned"})
	src.hub.Publish(Decision{ID: 2, Actor: 2, Profile: "yield", Kind: "Submitted"})
	src.hub.Publish(Decision{ID: 3, Actor: 1, Profile: "hodl", Kind: "Submitted"})
	src.hub.Publish(Decision{ID: 4, Actor: 1, Profile: "hodl", Kind: "Confirmed"})
	fr := readFrames(t, resp.Body, 2)
	if fr[0].id != "3" || fr[1].id != "4" || fr[0].event != "decision" {
		t.Fatalf("frames %+v", fr)
	}
	var d Decision
	if err := json.Unmarshal([]byte(fr[1].data), &d); err != nil || d.Kind != "Confirmed" {
		t.Fatalf("data %q: %v", fr[1].data, err)
	}
}

func waitSubscribers(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for h.Subscribers() != n {
		select {
		case <-h.Changes():
		case <-deadline:
			t.Fatalf("subscribers %d, want %d", h.Subscribers(), n)
		}
	}
}

func TestSSEReplaysFromLastEventID(t *testing.T) {
	srv, src := newServer(t, nil)
	for i := uint64(1); i <= 3; i++ {
		src.hub.Publish(Decision{ID: i * 10, Actor: 1, Kind: "Submitted"})
	}
	resp := openSSE(t, srv.URL+"/api/decisions", map[string]string{"Last-Event-ID": "10"})
	fr := readFrames(t, resp.Body, 2)
	if fr[0].id != "20" || fr[1].id != "30" {
		t.Fatalf("frames %+v", fr)
	}
	waitSubscribers(t, src.hub, 1)
	src.hub.Publish(Decision{ID: 40, Actor: 1, Kind: "Submitted"})
	if fr := readFrames(t, resp.Body, 1); fr[0].id != "40" {
		t.Fatalf("live %+v", fr)
	}
}

func TestSSEReportsAGap(t *testing.T) {
	src := &fakeSource{hub: NewHub(2, 2, 4)}
	t.Cleanup(src.hub.Close)
	srv := httptest.NewServer(NewHandler(Config{Source: src, Heartbeat: time.Hour}))
	t.Cleanup(srv.Close)
	for i := uint64(1); i <= 5; i++ {
		src.hub.Publish(Decision{ID: i, Kind: "Submitted"})
	}
	resp := openSSE(t, srv.URL+"/api/decisions?last_event_id=1", nil)
	fr := readFrames(t, resp.Body, 3)
	if fr[0].event != "gap" || fr[1].id != "4" || fr[2].id != "5" {
		t.Fatalf("frames %+v", fr)
	}
}

func TestSSEBadFilterAndClientCap(t *testing.T) {
	srv, src := newServer(t, nil)
	for _, q := range []string{"actor=x", "profile=A%20B", "kind=a-b", "last_event_id=x"} {
		if code, _ := get(t, srv.URL+"/api/decisions?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	r1 := openSSE(t, srv.URL+"/api/decisions", nil)
	r2 := openSSE(t, srv.URL+"/api/decisions", nil)
	waitSubscribers(t, src.hub, 2)
	if code, _ := get(t, srv.URL+"/api/decisions"); code != http.StatusTooManyRequests {
		t.Fatalf("third client: %d", code)
	}
	_ = r1.Body.Close()
	_ = r2.Body.Close()
	waitSubscribers(t, src.hub, 0)
}

func TestSSEDisconnectReleasesSubscription(t *testing.T) {
	srv, src := newServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/decisions", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	waitSubscribers(t, src.hub, 1)
	cancel()
	_ = resp.Body.Close()
	waitSubscribers(t, src.hub, 0)
}

func TestServeStopsOnCancelAndEndsStreams(t *testing.T) {
	src := &fakeSource{hub: NewHub(4, 2, 4)}
	defer src.hub.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, NewHandler(Config{Source: src, Heartbeat: time.Hour}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	resp := openSSE(t, "http://"+ln.Addr().String()+"/api/decisions", nil)
	waitSubscribers(t, src.hub, 1)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not stop with an open stream")
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Logf("stream ended: %v", err)
	}
}

func TestCapabilitiesReportControls(t *testing.T) {
	for name, tt := range map[string]struct {
		ctl  Controls
		want string
	}{
		"without": {nil, `{"controls":false,"explorer":false}`},
		"with":    {&fakeControls{}, `{"controls":true,"explorer":false}`},
	} {
		srv, _ := newServer(t, tt.ctl)
		code, body := get(t, srv.URL+"/api/capabilities")
		if code != http.StatusOK || strings.TrimSpace(string(body)) != tt.want {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
}

func TestUIServedBesideAPI(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ui:"+r.URL.Path) })
	src := &fakeSource{hub: NewHub(16, 2, 4)}
	t.Cleanup(src.hub.Close)
	srv := httptest.NewServer(NewHandler(Config{Source: src, UI: ui, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)

	if code, body := get(t, srv.URL+"/actors/3"); code != 200 || string(body) != "ui:/actors/3" {
		t.Errorf("client route: %d %s", code, body)
	}
	if code, body := get(t, srv.URL+"/api/nope"); code != http.StatusNotFound {
		t.Errorf("unknown api path reached the UI: %d %s", code, body)
	}
	if code, _ := get(t, srv.URL+"/api/overview"); code != 200 {
		t.Errorf("overview: %d", code)
	}
}

// GET /api/markets serves the source's Markets view.
func TestMarketsRoute(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Config{Source: &fakeSource{hub: NewHub(8, 4, 8)}}))
	defer srv.Close()
	code, body := get(t, srv.URL+"/api/markets")
	var m view.MarketsView
	if err := json.Unmarshal(body, &m); code != http.StatusOK || err != nil || m.Births["EUR"] != 2 || len(m.Flows) != 1 {
		t.Fatalf("markets %d %s %v", code, body, err)
	}
	var seeded struct{ Seeded string }
	if err := json.Unmarshal(body, &seeded); err != nil || seeded.Seeded != profile.SnapshotDate() {
		t.Fatalf("seeded %q, want the snapshot date %q (%v)", seeded.Seeded, profile.SnapshotDate(), err)
	}
}

// fakePeople serves one human.
type fakePeople struct{}

func (fakePeople) People(context.Context) (people.View, error) {
	return people.View{Humans: []people.Row{{ID: 3, Motivation: "hodl", Wallets: 2}}, Flows: []people.Flow{}}, nil
}

func (fakePeople) Person(_ context.Context, id uint64) (people.Person, error) {
	if id != 3 {
		return people.Person{}, fmt.Errorf("%w: human %d", view.ErrNotFound, id)
	}
	return people.Person{Row: people.Row{ID: 3}}, nil
}

func TestPeopleRoutes(t *testing.T) {
	bare := httptest.NewServer(NewHandler(Config{Source: &fakeSource{hub: NewHub(8, 4, 8)}}))
	defer bare.Close()
	if code, body := get(t, bare.URL+"/api/people"); code != http.StatusOK || !strings.Contains(string(body), `"humans":[]`) {
		t.Fatalf("people without a source: %d %s", code, body)
	}

	srv := httptest.NewServer(NewHandler(Config{Source: &fakeSource{hub: NewHub(8, 4, 8)}, People: fakePeople{}}))
	defer srv.Close()
	var v people.View
	code, body := get(t, srv.URL+"/api/people")
	if err := json.Unmarshal(body, &v); code != http.StatusOK || err != nil || len(v.Humans) != 1 || v.Humans[0].Wallets != 2 {
		t.Fatalf("people %d %s %v", code, body, err)
	}

	for path, want := range map[string]int{"/api/people/3": http.StatusOK, "/api/people/4": http.StatusNotFound, "/api/people/x": http.StatusBadRequest} {
		if code, _ := get(t, srv.URL+path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}
