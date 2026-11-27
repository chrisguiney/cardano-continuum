package sim

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// govCounts is what a run's governance intents settled: proposals by
// action type, votes by voter class, and vote delegations.
type govCounts struct {
	proposals   map[string]int
	votes       map[string]int
	delegations int
}

// countGov folds a log's confirmed governance intents.
func countGov(ctx context.Context, r eventlog.Reader) (govCounts, error) {
	g := govCounts{proposals: map[string]int{}, votes: map[string]int{"drep": 0, "spo": 0, "committee": 0}}
	sub := map[uint64]engine.Submitted{}
	err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		switch rec.Kind {
		case engine.KindSubmitted:
			var s engine.Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				return err
			}
			if s.Kind == string(profile.IntentPropose) || s.Kind == string(profile.IntentVote) || s.Kind == string(profile.IntentDelegateDRep) {
				sub[s.Intent] = s
			}
		case engine.KindConfirmed:
			var c engine.Confirmed
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				return err
			}
			s, ok := sub[c.Intent]
			if !ok {
				return nil
			}
			delete(sub, c.Intent)
			switch profile.IntentKind(s.Kind) {
			case profile.IntentPropose:
				g.proposals[cmpOr(s.Action, "info")]++
			case profile.IntentVote:
				g.votes[s.Voter]++
			case profile.IntentDelegateDRep:
				g.delegations++
			}
		}
		return nil
	})
	return g, err
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// line is the summary's governance line.
func (g govCounts) line() string {
	n := 0
	for _, c := range g.proposals {
		n += c
	}
	return fmt.Sprintf("governance: proposals %d (%s); votes %s; vote delegations %d\n",
		n, classCounts(g.proposals), classCounts(g.votes), g.delegations)
}
