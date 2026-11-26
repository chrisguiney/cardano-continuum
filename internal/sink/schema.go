// Package sink writes the engine's event log as Parquet tables for DuckDB and
// pandas, in the conventions of mithril2pq
// (https://github.com/chrisguiney/mithril2pq, README "Parquet schema") so one
// notebook joins both on tx_hash.
//
// The tables are flat (no nested types), keyed by the log's seq, and written
// as part-NNNNNN.parquet files under <dir>/<table>/ with an atomically
// replaced manifest naming the committed files. Export is incremental: a run
// appends only seqs above the manifest's last_seq.
package sink

import "github.com/apache/arrow-go/v18/arrow"

// SchemaVersion is bumped whenever a table or column changes. Notebook
// queries are written against one version; an export into a directory of
// another version is refused.
const SchemaVersion = 3

// Table names.
const (
	tDecision   = "decision"
	tScore      = "intent_score"
	tSubmission = "submission"
	tResult     = "submit_result"
	tFailure    = "submit_failure"
	tConfirm    = "confirmation"
	tTransition = "actor_transition"
	tBlock      = "block"
	tRollback   = "rollback"
	tRun        = "run"
	tFinding    = "finding"
)

// TableNames lists every table in write order.
var TableNames = []string{
	tDecision, tScore, tSubmission, tResult, tFailure,
	tConfirm, tTransition, tBlock, tRollback, tRun, tFinding,
}

// hash32 is a transaction or block hash: FIXED_LEN_BYTE_ARRAY(32), as in
// mithril2pq.
var hash32 = &arrow.FixedSizeBinaryType{ByteWidth: 32}

func col(name string, t arrow.DataType, null bool) arrow.Field {
	return arrow.Field{Name: name, Type: t, Nullable: null}
}

var meta = arrow.NewMetadata([]string{"continuum.schema_version"}, []string{"2"})

func mk(fields ...arrow.Field) *arrow.Schema { return arrow.NewSchema(fields, &meta) }

// schemas maps a table to its Arrow schema. seq is the log sequence number
// of the record a row came from.
var schemas = map[string]*arrow.Schema{
	tDecision: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("class", arrow.BinaryTypes.String, false),
		col("actor", arrow.PrimitiveTypes.Uint64, false),
		col("kind", arrow.BinaryTypes.String, false),
		col("intent_id", arrow.PrimitiveTypes.Uint64, true),
		col("intent_kind", arrow.BinaryTypes.String, true),
		col("chosen_score", arrow.PrimitiveTypes.Float64, true),
	),
	tScore: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("position", arrow.PrimitiveTypes.Uint32, false),
		col("intent_kind", arrow.BinaryTypes.String, false),
		col("target", arrow.PrimitiveTypes.Uint64, true),
		col("score", arrow.PrimitiveTypes.Float64, false),
		col("why", arrow.BinaryTypes.String, false),
	),
	tSubmission: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("actor", arrow.PrimitiveTypes.Uint64, false),
		col("intent_id", arrow.PrimitiveTypes.Uint64, false),
		col("attempt", arrow.PrimitiveTypes.Uint32, false),
		col("tx_hash", hash32, false),
		col("target", arrow.PrimitiveTypes.Uint64, false),
		col("amount", arrow.PrimitiveTypes.Uint64, false),
		col("fee", arrow.PrimitiveTypes.Uint64, false),
		col("ttl", arrow.PrimitiveTypes.Uint64, false),
		// kind is the intent's builder, null for a payment; action a
		// proposal's governance action type; contract the contract a lock
		// or unlock calls, null for contract 0 and every other kind.
		col("kind", arrow.BinaryTypes.String, true),
		col("action", arrow.BinaryTypes.String, true),
		col("contract", arrow.PrimitiveTypes.Uint64, true),
	),
	tResult: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("intent_id", arrow.PrimitiveTypes.Uint64, false),
		col("attempt", arrow.PrimitiveTypes.Uint32, true),
		col("tx_hash", hash32, true),
		col("verdict", arrow.BinaryTypes.String, false),
		col("reason", arrow.BinaryTypes.String, true),
		col("resend", arrow.FixedWidthTypes.Boolean, false),
	),
	tFailure: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("position", arrow.PrimitiveTypes.Uint32, false),
		col("class", arrow.BinaryTypes.String, false),
	),
	tConfirm: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("actor", arrow.PrimitiveTypes.Uint64, false),
		col("intent_id", arrow.PrimitiveTypes.Uint64, false),
		col("tx_hash", hash32, false),
	),
	tTransition: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("actor", arrow.PrimitiveTypes.Uint64, false),
		col("from_state", arrow.BinaryTypes.String, true),
		col("to_state", arrow.BinaryTypes.String, true),
		col("profile", arrow.BinaryTypes.String, true),
	),
	tBlock: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("slot", arrow.PrimitiveTypes.Uint64, false),
		col("hash", hash32, false),
	),
	tRollback: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("slot", arrow.PrimitiveTypes.Uint64, false),
		col("hash", hash32, true),
	),
	// finding is one oracle finding per row. source_seq is the input the
	// finding was derived from; verdicts are "impl@pool=verdict classes"
	// joined by ";", facts "name=value" joined by ";".
	tFinding: mk(
		col("seq", arrow.PrimitiveTypes.Uint64, false),
		col("at", arrow.PrimitiveTypes.Int64, false),
		col("id", arrow.BinaryTypes.String, false),
		col("kind", arrow.BinaryTypes.String, false),
		col("severity", arrow.BinaryTypes.String, false),
		col("source_seq", arrow.PrimitiveTypes.Uint64, false),
		col("summary", arrow.BinaryTypes.String, false),
		col("tx_hash", hash32, true),
		col("tx_cbor", arrow.BinaryTypes.Binary, true),
		col("expected", arrow.BinaryTypes.String, true),
		col("verdicts", arrow.BinaryTypes.String, true),
		col("facts", arrow.BinaryTypes.String, false),
		col("reproducible", arrow.FixedWidthTypes.Boolean, false),
		col("seed", arrow.PrimitiveTypes.Uint64, false),
		col("replay", arrow.BinaryTypes.String, true),
	),
	tRun: mk(
		col("format_version", arrow.PrimitiveTypes.Uint32, false),
		col("schema_version", arrow.PrimitiveTypes.Uint32, false),
		col("seed_fingerprint", arrow.BinaryTypes.Binary, false),
		col("profile_hash", arrow.BinaryTypes.Binary, false),
		col("genesis_hash", arrow.BinaryTypes.Binary, false),
		col("binary_version", arrow.BinaryTypes.String, false),
	),
}
