package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

func getEnums(t *testing.T) Enums {
	t.Helper()
	srv, _ := newServer(t, nil)
	code, body := get(t, srv.URL+"/api/meta/enums")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var e Enums
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// Each list the UI offers is the list the server validates against, so a
// value added to an allowlist without being exposed fails here.
func TestEnumsMatchAllowlists(t *testing.T) {
	e := getEnums(t)
	intents := append(strs(profile.IntentKinds()), strs(adversary.Names())...)
	slices.Sort(intents)
	want := map[string][]string{
		"actor_kind":    engine.ActorKinds(),
		"agenda":        strs(profile.Agendas()),
		"state":         strs(population.States()),
		"intent_kind":   intents,
		"finding_kind":  strs(oracle.Kinds()),
		"severity":      {"warn", "error", "fatal"},
		"impl":          strs(v1alpha1.Implementations()),
		"tier":          strs(v1alpha1.Tiers()),
		"phase":         strs(v1alpha1.Phases()),
		"attack":        strs(adversary.Names()),
		"decision_kind": engine.DecisionKinds(),
		"voter_class":   {"committee", "drep", "spo"},
		"action_type":   chain.ActionTypes(),
	}
	got := map[string][]string{
		"actor_kind": e.ActorKind, "agenda": e.Agenda, "state": e.State, "intent_kind": e.IntentKind,
		"finding_kind": e.FindingKind, "severity": e.Severity, "impl": e.Impl, "tier": e.Tier,
		"phase": e.Phase, "attack": e.Attack, "decision_kind": e.DecisionKind,
		"voter_class": e.VoterClass, "action_type": e.ActionType,
	}
	for k, w := range want {
		if len(w) == 0 || !slices.Equal(got[k], w) {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
}

// Every offered value is accepted by the server's own validators.
func TestEnumValuesAreAccepted(t *testing.T) {
	e := getEnums(t)
	for _, k := range e.ActorKind {
		r := httptest.NewRequest("GET", "/api/actors?kind="+k, nil)
		if q, err := parseActorQuery(r); err != nil || q.Filter.Kind != k {
			t.Errorf("actor kind %q: %v %+v", k, err, q.Filter)
		}
	}
	for _, s := range e.Severity {
		if _, err := parseFindingFilter(httptest.NewRequest("GET", "/api/findings?severity="+s, nil)); err != nil {
			t.Errorf("severity %q: %v", s, err)
		}
	}
	for _, k := range e.FindingKind {
		if _, err := parseFindingFilter(httptest.NewRequest("GET", "/api/findings?kind="+k, nil)); err != nil {
			t.Errorf("finding kind %q: %v", k, err)
		}
	}
	for _, k := range []string{"nonsense"} {
		if _, err := parseFindingFilter(httptest.NewRequest("GET", "/api/findings?kind="+k, nil)); err == nil {
			t.Errorf("finding kind %q accepted", k)
		}
	}
}

func TestFiltersByEnumValue(t *testing.T) {
	rows := []ActorRow{{ID: 1, Kind: "wallet"}, {ID: 2, Kind: "pool"}}
	pg := pageActors(rows, ActorQuery{Sort: "id", Limit: 10, Filter: ActorFilter{Kind: "pool"}})
	if pg.Total != 1 || pg.Rows[0].ID != 2 {
		t.Errorf("kind filter: %+v", pg)
	}
	if (Filter{Intent: "send"}).Match(Decision{Intent: "vote"}) || !(Filter{Intent: "send"}).Match(Decision{Intent: "send"}) {
		t.Error("intent filter")
	}
	if _, err := parseFilter(map[string][]string{"intent": {"bogus"}}); err == nil {
		t.Error("bogus intent accepted")
	}
}

// TestEnumsNameExUnitsKinds: the ExUnits attack, its finding and the two
// ExUnits mistakes are offered.
func TestEnumsNameExUnitsKinds(t *testing.T) {
	e := getEnums(t)
	for list, want := range map[string]struct {
		got  []string
		vals []string
	}{
		"attack":       {e.Attack, []string{"exunits-block-gap"}},
		"finding_kind": {e.FindingKind, []string{"block_exunits_over_limit"}},
		"mistake_kind": {e.MistakeKind, []string{"exunits_over_declared", "exunits_under_declared", "fee_too_small"}},
	} {
		for _, v := range want.vals {
			if !slices.Contains(want.got, v) {
				t.Errorf("%s lacks %q: %v", list, v, want.got)
			}
		}
	}
}
