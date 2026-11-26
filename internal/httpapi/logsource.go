package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Names reads the actor names and roles the store's read model holds.
// *store.DB satisfies it.
type Names interface {
	Actors(ctx context.Context, q store.ActorQuery) ([]store.ActorRow, error)
}

// Kube reads the operator's view of the network: its peer policy and the
// ContinuumPool objects with their observed status.
type Kube interface {
	Topology(ctx context.Context) (v1alpha1.TopologyMode, error)
	Pools(ctx context.Context) ([]v1alpha1.ContinuumPool, error)
}

// namesTTL is how long a read of the store's actor table is reused.
const namesTTL = 5 * time.Second

// LogSourceConfig wires a LogSource. Names, Kube, Slots and Now are
// optional: without them names are empty, the pool view is empty, the epoch
// is unset, and the clock is time.Now.
type LogSourceConfig struct {
	Projection *Projection
	Hub        *Hub
	Names      Names
	Kube       Kube
	Slots      *sched.Slots
	Now        func() time.Time
	// Findings, when set, serves the findings page (a live engine's
	// oracle.Set); without it the page is the findings the log holds.
	Findings Findings
	// Tips, when set, gives each pool's followed chain; a pool it does not
	// name falls back to the tip its ContinuumPool status records.
	Tips Tips
	// K is the network's security parameter and ForkDepth the number of
	// blocks two pools must differ on to be reported as forked.
	K, ForkDepth uint64
	// Control, when set, is the metrics of a control run (no adversaries)
	// on the same seed; the Adversaries page reports deltas against it.
	Control *adversary.Metrics
	// GovLifetime is the governance action lifetime in epochs, which dates
	// each proposal's last epoch.
	GovLifetime uint64
}

// Tips is what follows the pools' chains: each pool's newest blocks by
// pool name, oldest first.
type Tips interface {
	Tips() map[string][]chain.Mark
}

// LogSource is the read-only Source over an event log's projection, the
// store's actor table and the operator's pool status.
type LogSource struct {
	cfg LogSourceConfig

	mu     sync.Mutex // protects cached, idents, cacheT
	cached map[uint64]store.ActorRow
	idents map[uint64]store.Identity
	cacheT time.Time
}

// NewLogSource returns a LogSource.
func NewLogSource(cfg LogSourceConfig) *LogSource {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LogSource{cfg: cfg}
}

// Snapshot implements Source.
func (s *LogSource) Snapshot(ctx context.Context) (Snapshot, error) {
	n, err := s.names(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap := s.cfg.Projection.Snapshot(s.cfg.Slots)
	snap.Population.ByProfile = s.cfg.Projection.ProfileCounts(func(id uint64) string { return n[id].Profile })
	return snap, nil
}

// ActorTimeline implements Source.
func (s *LogSource) ActorTimeline(ctx context.Context, id uint64) (Timeline, error) {
	t, err := s.cfg.Projection.Timeline(id)
	if err != nil {
		return t, err
	}
	if n, err := s.names(ctx); err == nil {
		t.Role = n[id].Role
		if t.Profile == "" {
			t.Profile = n[id].Profile
		}
	}
	return t, nil
}

// Findings implements Source.
func (s *LogSource) Findings(_ context.Context, p Page) (FindingPage, error) {
	if s.cfg.Findings != nil {
		return FindingsPage(s.cfg.Findings, p), nil
	}
	return FindingsPage(s.cfg.Projection.findings, p), nil
}

// Subscribe implements Source.
func (s *LogSource) Subscribe(f Filter, after *uint64) (*Subscription, error) {
	return s.cfg.Hub.Subscribe(f, after)
}

// Governance implements Source. Genesis actors, which the log never
// spawns, take their profile from the store.
func (s *LogSource) Governance(ctx context.Context) (GovernanceView, error) {
	n, err := s.names(ctx)
	if err != nil {
		return GovernanceView{}, err
	}
	return s.cfg.Projection.Governance(s.cfg.Slots, s.cfg.GovLifetime, func(id uint64) string { return n[id].Profile }), nil
}

// Pools implements Source.
func (s *LogSource) Pools(ctx context.Context) (PoolsView, error) {
	if s.cfg.Kube == nil {
		return buildPools(nil, "", nil, 0, 0), nil
	}
	mode, err := s.cfg.Kube.Topology(ctx)
	if err != nil {
		return PoolsView{}, fmt.Errorf("read topology: %w", err)
	}
	ps, err := s.cfg.Kube.Pools(ctx)
	if err != nil {
		return PoolsView{}, fmt.Errorf("list pools: %w", err)
	}
	var tips map[string][]chain.Mark
	if s.cfg.Tips != nil {
		tips = s.cfg.Tips.Tips()
	}
	return buildPools(ps, mode, tips, s.cfg.K, s.cfg.ForkDepth), nil
}

// Actors implements Source. The log decides which actors exist; the store
// adds their name and role.
func (s *LogSource) Actors(ctx context.Context, q ActorQuery) (ActorPage, error) {
	if !slices.Contains(SortKeys, q.Sort) {
		return ActorPage{}, fmt.Errorf("%w: unknown sort key %q", ErrBadRequest, q.Sort)
	}
	if q.Limit < 1 || q.Limit > MaxLimit || q.Offset < 0 {
		return ActorPage{}, fmt.Errorf("%w: page limit %d offset %d", ErrBadRequest, q.Limit, q.Offset)
	}
	rows := s.cfg.Projection.Rows()
	n, err := s.names(ctx)
	if err != nil {
		return ActorPage{}, err
	}
	idents := s.identities()
	for i := range rows {
		rows[i].Name, rows[i].Role = n[rows[i].ID].Name, n[rows[i].ID].Role
		if rows[i].Profile == "" {
			rows[i].Profile = n[rows[i].ID].Profile
		}
		if in, ok := idents[rows[i].ID]; ok {
			rows[i].withIdentity(in)
		}
	}
	return pageActors(rows, q), nil
}

// names returns the store's actor table by id, reusing a read for namesTTL.
func (s *LogSource) names(ctx context.Context) (map[uint64]store.ActorRow, error) {
	if s.cfg.Names == nil {
		return nil, nil
	}
	s.mu.Lock()
	if s.cached != nil && s.cfg.Now().Sub(s.cacheT) < namesTTL {
		c := s.cached
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	all := map[uint64]store.ActorRow{}
	for off := 0; ; off += store.MaxLimit {
		page, err := s.cfg.Names.Actors(ctx, store.ActorQuery{SortBy: "id", Offset: off, Limit: store.MaxLimit})
		if err != nil {
			return nil, fmt.Errorf("read actor names: %w", err)
		}
		for _, r := range page {
			all[r.ID] = r
		}
		if len(page) < store.MaxLimit {
			break
		}
	}
	s.mu.Lock()
	s.cached, s.idents, s.cacheT = all, parseIdents(all), s.cfg.Now()
	s.mu.Unlock()
	return all, nil
}

// identities returns the identities parsed from the last names read. A row
// whose identity does not parse is skipped: one bad row must not hide the
// table.
func (s *LogSource) identities() map[uint64]store.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idents
}

func parseIdents(rows map[uint64]store.ActorRow) map[uint64]store.Identity {
	out := make(map[uint64]store.Identity, len(rows))
	for id, r := range rows {
		if r.Identity == "" {
			continue
		}
		if in, err := store.DecodeIdentity(r.Identity); err == nil {
			out[id] = in
		}
	}
	return out
}

// pageActors filters, sorts and slices rows. Sorting is stable on id, so a
// page boundary never repeats or skips a row.
func pageActors(rows []ActorRow, q ActorQuery) ActorPage {
	rows = slices.DeleteFunc(rows, func(r ActorRow) bool {
		f := q.Filter
		return (f.Profile != "" && r.Profile != f.Profile) ||
			(f.State != "" && r.State != f.State) ||
			(f.Role != "" && r.Role != f.Role) ||
			(f.Kind != "" && r.Kind != f.Kind) ||
			(f.Delegated != nil && r.Delegated != *f.Delegated)
	})
	slices.SortStableFunc(rows, func(a, b ActorRow) int {
		c := compareActors(q.Sort, a, b)
		if q.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return cmpUint(a.ID, b.ID)
	})
	out := ActorPage{Total: len(rows), Offset: q.Offset, Limit: q.Limit, Rows: []ActorRow{}}
	if q.Offset < len(rows) {
		out.Rows = rows[q.Offset:min(q.Offset+q.Limit, len(rows))]
	}
	return out
}

func compareActors(key string, a, b ActorRow) int {
	switch key {
	case "name":
		return strings.Compare(a.Name, b.Name)
	case "role":
		return strings.Compare(a.Role, b.Role)
	case "profile":
		return strings.Compare(a.Profile, b.Profile)
	case "state":
		return strings.Compare(a.State, b.State)
	case "intents":
		return a.Intents - b.Intents
	case "txs":
		return a.Txs - b.Txs
	case "retries":
		return a.Retries - b.Retries
	case "confirmed":
		return a.Confirmed - b.Confirmed
	case "released":
		return a.Released - b.Released
	case "kind":
		return strings.Compare(a.Kind, b.Kind)
	case "balance":
		return cmpUint(a.Balance, b.Balance)
	case "tokens":
		return a.Tokens - b.Tokens
	}
	return cmpUint(a.ID, b.ID)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

var (
	_ Source = (*LogSource)(nil)
	_ Names  = (*store.DB)(nil)
)

// ActorDetail implements Source.
func (s *LogSource) ActorDetail(ctx context.Context, id uint64) (ActorDetail, error) {
	l, err := s.cfg.Projection.ActorLog(id)
	if err != nil {
		return ActorDetail{}, err
	}
	n, err := s.names(ctx)
	if err != nil {
		return ActorDetail{}, err
	}
	row := n[id]
	d := ActorDetail{ID: id, Name: row.Name, Profile: l.info.profile, Timeline: l.timeline, Findings: s.actorFindings(id)}
	if d.Profile == "" {
		d.Profile = row.Profile
	}
	in, err := store.DecodeIdentity(row.Identity)
	if err != nil {
		return ActorDetail{}, fmt.Errorf("actor %d: %w", id, err)
	}
	d.HasIdentity = row.Identity != ""
	d.Kind, d.Agenda = in.Kind, in.Agenda
	d.Wallet = WalletIDs{in.PaymentAddr, in.StakeAddr, in.PaymentKeyHash, in.StakeKeyHash, in.DRepID, in.PoolID}
	d.Holdings = Holdings{in.Settled, in.Reserved, in.Unconfirmed, in.UTxOs, in.InFlight, in.Tokens}
	if d.Holdings.Tokens == nil {
		d.Holdings.Tokens = []store.Token{}
	}
	d.Delegation = Delegation{in.StakeRegistered, in.DelegatedPool, in.DelegatedActor, in.DelegatedDRep}
	d.Traits = Traits{in.Competence, in.Care, in.Patience, in.LearningRate}
	d.Lifecycle = Lifecycle{State: l.info.state, BornMs: l.info.born, ChangedMs: l.info.changed, Frustration: in.Frustration, Hazard: in.Hazard}
	i := l.info
	d.Activity = Activity{Intents: i.intents, Submitted: i.txs, Confirmed: i.confirmed, Refused: i.refused, Released: i.released,
		GaveUp: i.gaveUp, Retries: i.retries, LastMs: i.last, LostLovelace: i.lost, Recent: i.recent}
	if d.Activity.Recent == nil {
		d.Activity.Recent = []Decision{}
	}
	if in.CostCeiling > 0 {
		d.Adversary = &AdversaryCost{in.CostSpent, in.CostCeiling, in.Metric}
	}
	return d, nil
}

// actorFindings lists the findings whose transaction the actor submitted.
func (s *LogSource) actorFindings(id uint64) []ActorFinding {
	fs := s.findingsSet()
	_, n := fs.Page(0, 0)
	all, _ := fs.Page(0, n)
	out := []ActorFinding{}
	for _, f := range all {
		if owner, ok := s.cfg.Projection.TxOwner(f.TxHash); ok && owner == id {
			out = append(out, ActorFinding{ID: f.ID, Seq: f.Seq, Kind: string(f.Kind), Severity: f.Severity.String(), Summary: strings.TrimSpace(f.Summary)})
		}
	}
	return out
}

func (s *LogSource) findingsSet() Findings {
	if s.cfg.Findings != nil {
		return s.cfg.Findings
	}
	return s.cfg.Projection.findings
}

// Adversaries implements Source.
func (s *LogSource) Adversaries(ctx context.Context) (AdversaryView, error) {
	stats, cur, now := s.cfg.Projection.Attacks()
	n, err := s.names(ctx)
	if err != nil {
		return AdversaryView{}, err
	}
	idents := s.identities()
	ids := map[uint64]bool{}
	for _, st := range stats {
		ids[st.Actor] = true
	}
	for id, in := range idents {
		if in.Kind == "adversary" {
			ids[id] = true
		}
	}
	var ctl adversary.Metrics
	if s.cfg.Control != nil {
		ctl = *s.cfg.Control
	}
	impact := impactRows(ctl, cur, s.cfg.Control != nil)
	out := AdversaryView{HasControl: s.cfg.Control != nil, Adversaries: []AdversaryRow{}, Impact: impact}
	for _, id := range slices.Sorted(mapKeys(ids)) {
		var st engine.AttackStat
		for _, x := range stats {
			if x.Actor == id {
				st = x
			}
		}
		out.Adversaries = append(out.Adversaries, s.adversaryRow(id, st, n[id], idents[id], impact, now))
	}
	return out, nil
}

func (s *LogSource) adversaryRow(id uint64, st engine.AttackStat, row store.ActorRow, in store.Identity, impact []ImpactRow, now int64) AdversaryRow {
	r := AdversaryRow{ID: id, Name: row.Name, Profile: row.Profile, Attack: st.Name, Agenda: in.Agenda, Budget: in.Budget,
		Attempted: st.Steps, Landed: st.Landed, Detected: st.Detected, Rejected: st.Rejected,
		Recent: []AttackStepRow{}, Findings: s.adversaryFindings(id)}
	if r.Attack == "" {
		r.Attack = strings.TrimPrefix(row.Profile, "adversary-")
	}
	for _, x := range st.Recent {
		r.Recent = append(r.Recent, AttackStepRow{Seq: x.Seq, At: x.At, Intent: x.Intent, Mutation: x.Mutation, Cost: x.Cost, Value: x.Value, Txs: x.Txs})
	}
	r.Cost = s.costView(st, in.CostCeiling, now)
	r.Metric = SuccessView{Name: in.Metric}
	if r.Metric.Name == "" {
		r.Metric.Name = adversary.SuccessMetric(adversary.Name(r.Attack))
	}
	for _, i := range impact {
		if i.Metric == r.Metric.Name {
			r.Metric.Value, r.Metric.Control = i.Value, i.Control
		}
	}
	return r
}

func (s *LogSource) costView(st engine.AttackStat, ceiling uint64, now int64) CostView {
	c := CostView{Spent: st.Cost, Ceiling: ceiling, ByEpoch: []EpochCost{}}
	epochOf := func(int64) uint64 { return 0 }
	if s.cfg.Slots != nil {
		epochOf = func(at int64) uint64 { return s.cfg.Slots.EpochAt(sched.Time(at)) }
	}
	c.Epoch = epochOf(now)
	if n := len(st.Costs); n > 0 {
		c.Epoch = epochOf(st.Costs[n-1].At)
	}
	for _, p := range st.Costs {
		e := epochOf(p.At)
		if n := len(c.ByEpoch); n > 0 && c.ByEpoch[n-1].Epoch == e {
			c.ByEpoch[n-1].Spent += p.Cost
		} else {
			c.ByEpoch = append(c.ByEpoch, EpochCost{Epoch: e, Spent: p.Cost})
		}
		if e == c.Epoch {
			c.EpochSpent += p.Cost
		}
	}
	return c
}

// adversaryFindings lists the findings on transactions the adversary sent.
func (s *LogSource) adversaryFindings(id uint64) []ActorFinding {
	fs := s.findingsSet()
	_, n := fs.Page(0, 0)
	all, _ := fs.Page(0, n)
	out := []ActorFinding{}
	for _, f := range all {
		if owner, ok := s.cfg.Projection.attackOwner(f.TxHash); ok && owner == id {
			out = append(out, ActorFinding{ID: f.ID, Seq: f.Seq, Kind: string(f.Kind), Severity: f.Severity.String(), Summary: strings.TrimSpace(f.Summary)})
		}
	}
	return out
}
