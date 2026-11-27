// Package eventlog is the append-only record of every external input to the
// engine and every decision the engine took from them, plus the replay that
// proves the decisions follow from the inputs.
//
// The record codec, diffing and replay are pure. File and in-memory logs are
// the I/O shell. The log goes first: an input is appended and synced before
// any effect or checkpoint reflects it, so recovery is checkpoint plus a
// replay of the tail (Scan from checkpoint seq + 1).
package eventlog
