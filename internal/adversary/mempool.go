package adversary

import "math"

// Mempool exhaustion. An adversary fills the mempool with large, high-fee
// transactions near the block-size limit, so honest transactions wait behind
// the flood. The impact is the honest confirmation latency, measured the same
// way continuum compare measures a live run: a control Metrics with no flood
// and a treated one with it, turned into an impact table by Compare.
//
// The model is a deterministic queue, not the fake ledger: each slot a block
// takes transactions up to the body-size budget, highest fee first. A flood of
// higher-fee transactions therefore sits ahead of the honest ones until it
// drains, which is the latency the honest user sees.

// MempoolFlood is one exhaustion scenario.
type MempoolFlood struct {
	// BlockBytes is the body-size budget one block can include (MaxBlockBodySize).
	BlockBytes uint64
	// SlotSeconds is how long a slot lasts; one block is produced per active
	// slot, which this model takes as every slot.
	SlotSeconds float64
	// HonestTxBytes is the size of each honest transaction.
	HonestTxBytes uint64
	// HonestTxs is how many honest transactions are submitted at the start.
	HonestTxs int
	// FloodBytes is the total bytes of adversary transactions submitted ahead
	// of the honest ones; zero is the control.
	FloodBytes uint64
	// FloodOutbids is true when the flood pays a higher fee, so it is included
	// before the honest transactions. A flood that does not outbid does not
	// delay them.
	FloodOutbids bool
}

// Measure returns the honest latency Metrics without the flood (control) and
// with it (treated).
func (f MempoolFlood) Measure() (control, treated Metrics) {
	return f.run(0), f.run(f.FloodBytes)
}

// run simulates the honest transactions behind flood bytes and records each
// one's confirmation latency.
func (f MempoolFlood) run(flood uint64) Metrics {
	budget := f.BlockBytes
	if budget == 0 {
		budget = 1
	}
	// Bytes that sit ahead of the honest transactions before any are included.
	var ahead uint64
	if f.FloodOutbids {
		ahead = flood
	}
	m := Metrics{HonestIntents: f.HonestTxs, HonestEnded: f.HonestTxs}
	offset := ahead
	for range f.HonestTxs {
		offset += f.HonestTxBytes
		// A transaction whose last byte falls in block b (1-based) confirms at
		// the end of slot b.
		block := uint64(math.Ceil(float64(offset) / float64(budget)))
		m.Latency = append(m.Latency, float64(block)*f.SlotSeconds)
	}
	return m
}

// Impact is the honest-latency impact of the flood, as Compare rows.
func (f MempoolFlood) Impact() []Impact {
	control, treated := f.Measure()
	return Compare(control, treated)
}
