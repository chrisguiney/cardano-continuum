// Package tla turns TLA+ model-checker output into inputs for adversaries.
//
// It has three parts. The decoder reads TLC's JSON value encoding into [Value]
// and counterexample traces into [Trace]. The harvester ([Harvest]) fetches a
// model repository at a pinned commit, runs TLC on every *Bug.cfg and writes
// one scenario-seed file per cfg. The vector exporter ([ExportVectors]) dumps
// every transaction that Utxo.tla's TableSpec enumerates, with the verdict
// SpecAccepts gives it.
//
// # TLC version
//
// TLC 2.19 (tla2tools 1.7.4, the version models/scripts/install-tla.sh pins as
// the default tools) has no -dumpTrace option and fails with "unrecognized
// option". The harvester therefore runs the pinned 1.8.0 jar
// (tla2tools-1.8.0.jar), selected through TLA2TOOLS_JAR, and uses
//
//	tlc -noGenerateSpecTE -dumpTrace json FILE -config X.cfg X.tla
//
// -noGenerateSpecTE stops TLC from writing *_TTrace_* files next to the specs.
//
// # Vector export method
//
// Utxo.tla's SpecAccepts reads the ledger variables (utxo, slot, registered,
// reward), so a vector's verdict depends on the ledger state as well as the
// transaction. TableSpec's reachable states are exactly the small ledgers.
// ExportVectors writes a wrapper module, UtxoVectors, that EXTENDS Utxo and
// Json and defines an invariant, VecDump, which is true in every state. As a
// side effect each evaluation calls JsonSerialize to write that state's ledger
// and every expressible transaction of the five families with its SpecAccepts
// verdict, to a file named after the state. This needs no ALIAS and no change
// to the model, and TLC evaluates the invariant once per distinct state. The
// per-family cardinalities, computed by TLC on the sets themselves, travel in
// each file so the row count can be checked against TLC's distinct-state
// count: rows = distinct states x sum of cardinalities.
//
// The vector file is JSON Lines, one [Vector] per line, in a stable order so a
// regeneration at the same commit produces no diff. Each row repeats its
// ledger, so the whole table is large (Babbage alone is about 1.07 million
// rows, 430 MB): generate it on demand rather than committing it.
package tla
