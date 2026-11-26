package view

import (
	"encoding/hex"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// ToDecision turns a Decision-class record into a feed entry. The body is
// decoded generically so a new decision kind reaches the feed without a
// change here; IntentChosen also exposes the chosen kind and its scores.
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
		d.Intent, d.Scores = string(dec.Intent.Kind), dec.Scores
		return d, nil
	}
	if r.Kind == engine.KindSubmitted {
		var s engine.Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return d, fmt.Errorf("decision seq %d: %w", r.Seq, err)
		}
		d.Intent = s.Kind
	}
	var v any
	if err := eventlog.Decode(r.Body, &v); err != nil {
		return d, fmt.Errorf("decision seq %d: %w", r.Seq, err)
	}
	d.Body = jsonable(v)
	return d, nil
}

// jsonable rewrites a generically decoded CBOR value into one encoding/json
// can marshal: integer-keyed maps get string keys and byte strings become
// hex.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonable(e)
		}
		return m
	case []any:
		for i := range x {
			x[i] = jsonable(x[i])
		}
		return x
	case []byte:
		return hex.EncodeToString(x)
	default:
		return v
	}
}
