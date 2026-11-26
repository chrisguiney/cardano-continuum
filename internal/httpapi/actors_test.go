package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func identRow(t testing.TB, id uint64, name, state string, in store.Identity) store.ActorRow {
	t.Helper()
	s, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return store.ActorRow{ID: id, Name: name, Role: state, Profile: "hodl", Identity: s}
}

func richSource(t *testing.T) *LogSource {
	t.Helper()
	h2 := txid(0xa2)
	f := oracle.Finding{ID: "f1", Kind: oracle.UnexpectedAccept, Severity: oracle.Fatal, Seq: 9, Summary: "took it", TxHash: h2[:]}
	rec, err := f.Record(100)
	if err != nil {
		t.Fatal(err)
	}
	tl, p, h := newTail(t, writeLog(t, append(fixture(t), rec)))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	a1 := store.Identity{
		Kind: "wallet", Agenda: "hodl", PaymentAddr: "addr_test1qz" + strings.Repeat("x", 80), StakeAddr: "stake_test1uz" + strings.Repeat("y", 40),
		PaymentKeyHash: strings.Repeat("ab", 28), StakeKeyHash: strings.Repeat("cd", 28),
		Competence: 0.9, Care: 0.5, Patience: 2, LearningRate: 0.1, Frustration: 0.3, Hazard: 0.002,
		Settled: 50_000_000, UTxOs: 3, Reserved: 5_000_000, InFlight: 1, Unconfirmed: 5_000_000,
		Tokens:          []store.Token{{Policy: strings.Repeat("11", 28), Name: "6162", NameText: "ab", Quantity: 7}},
		StakeRegistered: true, DelegatedPool: "pool1abc", DelegatedDRep: "always_abstain",
	}
	a2 := store.Identity{Kind: "adversary", Agenda: "profit", PaymentAddr: "addr_test1qq" + strings.Repeat("z", 80), Settled: 9_000_000, CostSpent: 3, CostCeiling: 10, Metric: "stolen"}
	names := fakeNames{rows: []store.ActorRow{identRow(t, 1, "hodl-1", "active", a1), identRow(t, 2, "adv-2", "active", a2)}}
	return NewLogSource(LogSourceConfig{Projection: p, Hub: h, Names: names, Now: time.Now})
}

func TestActorRowsCarryKeyColumns(t *testing.T) {
	src := richSource(t)
	pg, err := src.Actors(context.Background(), ActorQuery{Sort: "id", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	r := pg.Rows[0]
	if r.Kind != "wallet" || r.Balance != 50_000_000 || r.Pending != 5_000_000 || r.Tokens != 1 || !r.Delegated || r.Pool != "pool1abc" || r.DRep != "always_abstain" {
		t.Fatalf("row %+v", r)
	}
	if !strings.HasPrefix(r.Address, "addr_test1qz") || !strings.Contains(r.Address, "…") || len(r.Address) > 24 {
		t.Fatalf("short address %q", r.Address)
	}
	if pg.Rows[1].Delegated || pg.Rows[1].Kind != "adversary" {
		t.Fatalf("row 2 %+v", pg.Rows[1])
	}
}

func TestActorsSortByBalanceAndFilterDelegated(t *testing.T) {
	src, ctx := richSource(t), context.Background()
	pg, err := src.Actors(ctx, ActorQuery{Sort: "balance", Desc: true, Limit: 10})
	if err != nil || pg.Rows[0].ID != 1 {
		t.Fatalf("balance desc %+v %v", pg, err)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "balance", Limit: 10})
	if pg.Rows[0].ID != 2 {
		t.Fatalf("balance asc %+v", pg)
	}
	yes, no := true, false
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "id", Limit: 10, Filter: ActorFilter{Delegated: &yes}})
	if pg.Total != 1 || pg.Rows[0].ID != 1 {
		t.Fatalf("delegated %+v", pg)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "id", Limit: 10, Filter: ActorFilter{Delegated: &no}})
	if pg.Total != 1 || pg.Rows[0].ID != 2 {
		t.Fatalf("not delegated %+v", pg)
	}
}

func TestActorDetailSections(t *testing.T) {
	d, err := richSource(t).ActorDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 1 || d.Kind != "wallet" || d.Agenda != "hodl" || d.Profile != "hodl" || !d.HasIdentity {
		t.Fatalf("identity %+v", d)
	}
	if d.Wallet.PaymentAddr == "" || d.Wallet.StakeKeyHash != strings.Repeat("cd", 28) {
		t.Fatalf("wallet %+v", d.Wallet)
	}
	h := d.Holdings
	if h.SettledLovelace != 50_000_000 || h.ReservedLovelace != 5_000_000 || h.UTxOs != 3 || h.InFlight != 1 || len(h.Tokens) != 1 || h.Tokens[0].Quantity != 7 {
		t.Fatalf("holdings %+v", h)
	}
	if !d.Delegation.StakeRegistered || d.Delegation.Pool != "pool1abc" || d.Delegation.DRep != "always_abstain" {
		t.Fatalf("delegation %+v", d.Delegation)
	}
	if d.Lifecycle.State != "active" || d.Lifecycle.BornMs != 0 || d.Lifecycle.ChangedMs != 5000 || d.Lifecycle.Hazard != 0.002 || d.Lifecycle.Frustration != 0.3 {
		t.Fatalf("lifecycle %+v", d.Lifecycle)
	}
	if d.Traits.Competence != 0.9 || d.Traits.Patience != 2 {
		t.Fatalf("traits %+v", d.Traits)
	}
	a := d.Activity
	if a.Intents != 1 || a.Submitted != 2 || a.Confirmed != 1 || a.Refused != 1 || a.Retries != 1 || a.LastMs != 20000 || len(a.Recent) != 1 || a.Recent[0].Intent != "send" || len(a.Recent[0].Scores) != 2 {
		t.Fatalf("activity %+v", a)
	}
	if len(d.Findings) != 1 || d.Findings[0].ID != "f1" {
		t.Fatalf("findings %+v", d.Findings)
	}
	if d.Adversary != nil || len(d.Timeline) == 0 {
		t.Fatalf("adversary %+v timeline %d", d.Adversary, len(d.Timeline))
	}
	adv, err := richSource(t).ActorDetail(context.Background(), 2)
	if err != nil || adv.Adversary == nil || adv.Adversary.Spent != 3 || adv.Adversary.Ceiling != 10 || adv.Adversary.Metric != "stolen" {
		t.Fatalf("adversary %+v %v", adv.Adversary, err)
	}
}

func TestActorDetailUnknownActor(t *testing.T) {
	if _, err := richSource(t).ActorDetail(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

// Without a store there is no identity: the detail still answers from the
// log and says so.
func TestActorDetailWithoutIdentity(t *testing.T) {
	tl, p, h := newTail(t, writeLog(t, fixture(t)))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := NewLogSource(LogSourceConfig{Projection: p, Hub: h}).ActorDetail(context.Background(), 1)
	if err != nil || d.HasIdentity || d.Activity.Submitted != 2 || d.Holdings.Tokens == nil {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestActorQueryAllowlists(t *testing.T) {
	srv, src := newServer(t, nil)
	for _, q := range []string{"sort=secret", "delegated=maybe", "delegated=1;x", "sort=balance;x"} {
		if code, body := get(t, srv.URL+"/api/actors?"+q); code != 400 {
			t.Errorf("%s: %d %s", q, code, body)
		}
	}
	if code, _ := get(t, srv.URL+"/api/actors?sort=balance&dir=desc&delegated=true"); code != 200 {
		t.Fatal(code)
	}
	if src.lastQ.Sort != "balance" || src.lastQ.Filter.Delegated == nil || !*src.lastQ.Filter.Delegated {
		t.Fatalf("query %+v", src.lastQ)
	}
	if code, _ := get(t, srv.URL+"/api/actors/x"); code != 400 {
		t.Fatal("non-numeric id accepted")
	}
	if code, _ := get(t, srv.URL+"/api/actors/99"); code != 404 {
		t.Fatal("unknown id not 404")
	}
}

func TestActorDetailGolden(t *testing.T) {
	srv, _ := newServer(t, nil)
	code, body := get(t, srv.URL+"/api/actors/1")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	golden(t, "actor", body)
}

// A page of a 10k-actor table must come back fast: the table is sorted and
// filtered per request.
func TestActorsPagingAtScale(t *testing.T) {
	const n = 10_000
	rows := make([]ActorRow, n)
	for i := range rows {
		rows[i] = ActorRow{ID: uint64(i + 1), State: "active", Balance: uint64((i * 7919) % 100_003), Delegated: i%3 == 0}
	}
	yes := true
	start := time.Now()
	pg := pageActors(rows, ActorQuery{Sort: "balance", Desc: true, Offset: 9_000, Limit: 100, Filter: ActorFilter{Delegated: &yes}})
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("took %v", el)
	}
	if pg.Total != 3334 || len(pg.Rows) != 0 {
		t.Fatalf("total %d rows %d", pg.Total, len(pg.Rows))
	}
	pg = pageActors(rows, ActorQuery{Sort: "balance", Desc: true, Offset: 0, Limit: 100})
	if len(pg.Rows) != 100 || pg.Rows[0].Balance < pg.Rows[99].Balance {
		t.Fatalf("not sorted: %d %d", pg.Rows[0].Balance, pg.Rows[99].Balance)
	}
}

// The detail response has a fixed set of top-level sections, so nothing
// beyond the store's public identity and the log's counters can ride on it.
func TestActorResponsesAddNoUnstoredBytes(t *testing.T) {
	src := richSource(t)
	d, err := src.ActorDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "id", "name", "profile", "kind", "agenda", "has_identity", "wallet", "holdings", "delegation", "lifecycle", "traits", "activity", "findings", "adversary", "timeline":
		default:
			t.Errorf("unexpected field %q", k)
		}
	}
}
