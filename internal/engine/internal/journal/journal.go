// Package journal buffers the decisions one engine input causes, in order.
package journal

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Journal is the decision buffer of one Handle call: the records the input
// caused in order, the sequence number of the input and the intents it
// ended.
type Journal struct {
	out  []eventlog.Record
	seq  uint64
	ends []oracle.End
}

// Begin starts the buffer for the input numbered seq.
func (j *Journal) Begin(seq uint64) { j.out, j.ends, j.seq = nil, nil, seq }

// Seq is the number of the input being handled.
func (j *Journal) Seq() uint64 { return j.seq }

// Emit appends one decision.
func (j *Journal) Emit(at sched.Time, kind string, a population.ActorID, body any) {
	raw, err := eventlog.Encode(body)
	if err != nil {
		// Bodies are fixed structs of plain fields; only a NaN or infinite
		// float fails to encode, and producing one is an engine bug.
		panic(fmt.Sprintf("engine: encode %s: %v", kind, err))
	}
	j.out = append(j.out, eventlog.Record{At: at, Class: eventlog.Decision, Kind: kind, Actor: eventlog.ActorID(a), Body: raw})
}

// Append adds a record built elsewhere (an oracle finding).
func (j *Journal) Append(rec eventlog.Record) { j.out = append(j.out, rec) }

// End notes an intent settling or written off in this input, for the
// paid-once monitor.
func (j *Journal) End(id uint64, tx wire.TxID, released bool) {
	j.ends = append(j.ends, oracle.End{Intent: id, Tx: chain.TxID(tx), Released: released, Seq: j.seq})
}

// Ends are the intents the input ended.
func (j *Journal) Ends() []oracle.End { return j.ends }

// Drop discards the buffered records, for an input that failed or that is
// configuration rather than decision.
func (j *Journal) Drop() { j.out = nil }

// Take returns the buffered records and empties the buffer.
func (j *Journal) Take() []eventlog.Record {
	out := j.out
	j.out = nil
	return out
}
