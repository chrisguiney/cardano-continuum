package profile

import "math"

// maxBodyDraws bounds the rejection loop that keeps a body draw below the
// tail scale. The body is nearly always well below it, so the bound is never
// reached in practice; it keeps Sample's cost finite for any parameters.
const maxBodyDraws = 32

// Amount is a payment-size distribution in lovelace: a log-normal body with
// an optional Pareto tail. With a tail, a draw comes from the tail with
// probability TailShare, otherwise from the body truncated at the tail's
// scale, so the two never overlap and TailShare is the probability of a
// payment above Tail.Xm.
type Amount struct {
	Body      LogNormal
	Tail      *Pareto
	TailShare float64
}

// Sample draws one payment size in lovelace.
func (a Amount) Sample(src Source) float64 {
	if a.Tail == nil {
		return a.Body.Sample(src)
	}
	if src.Float64() < a.TailShare {
		return a.Tail.Sample(src)
	}
	for range maxBodyDraws {
		if x := a.Body.Sample(src); x < a.Tail.Xm {
			return x
		}
	}
	return math.Nextafter(a.Tail.Xm, 0)
}
