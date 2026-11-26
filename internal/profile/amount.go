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

func (ra rawAmount) validate() (*Amount, error) {
	d, err := parseDist(ra.Body)
	if err != nil {
		return nil, fieldErr("amount.body", "%v", err)
	}
	body, ok := d.(LogNormal)
	if !ok {
		return nil, fieldErr("amount.body", "must be a lognormal distribution, got %s", d)
	}
	a := &Amount{Body: body}
	if ra.Tail == "" {
		if ra.TailShare != 0 {
			return nil, fieldErr("amount.tail_share", "needs amount.tail")
		}
		return a, nil
	}
	d, err = parseDist(ra.Tail)
	if err != nil {
		return nil, fieldErr("amount.tail", "%v", err)
	}
	tail, ok := d.(Pareto)
	if !ok {
		return nil, fieldErr("amount.tail", "must be a pareto distribution, got %s", d)
	}
	if !(ra.TailShare > 0 && ra.TailShare <= 1) {
		return nil, fieldErr("amount.tail_share", "must be in (0, 1], got %s", ff(ra.TailShare))
	}
	a.Tail, a.TailShare = &tail, ra.TailShare
	return a, nil
}
