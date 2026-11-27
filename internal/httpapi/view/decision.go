package view

import (
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// ToDecision turns a Decision-class record into a feed entry. The body is
// passed through as the log's JSON, checked to parse, so a new decision
// kind reaches the feed without a change here; IntentChosen also exposes the chosen kind and its scores.
func ToDecision(r eventlog.Record, profile string) (Decision, error) {
	d := Decision{ID: r.Seq, At: int64(r.At), Kind: r.Kind, Actor: uint64(r.Actor), Profile: profile}
	if len(r.Body) == 0 {
		return d, nil
	}
	if r.Kind == engine.KindIntentChosen {
		var dec agenda.Decision
		if err := eventlog.Decode(r.Body, &dec); err != nil {
			return d, fmt.Errorf("decision seq %d: %w", r.Seq, err)
		}
		d.Intent, d.Scores, d.Clock = string(dec.Intent.Kind), dec.Scores, string(dec.Clock)
		return d, nil
	}
	if r.Kind == engine.KindSubmitted {
		var s engine.Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return d, fmt.Errorf("decision seq %d: %w", r.Seq, err)
		}
		d.Intent = s.Kind
	}
	if err := eventlog.Decode(r.Body, new(jsontext.Value)); err != nil {
		return d, fmt.Errorf("decision seq %d: %w", r.Seq, err)
	}
	// The body is the log's JSON already; a generic decode would turn
	// lovelace above 2^53 into a rounded float.
	d.Body = json.RawMessage(r.Body)
	return d, nil
}
