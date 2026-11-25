//go:build race

package engine

// crashStride samples the crash sweep under the race detector, which slows
// each run about tenfold; the full sweep runs without it.
const crashStride = 10
