package submit

import (
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
)

// NewFake returns the fake chain as a submit target.
func NewFake(c *fakechain.Chain) Target { return c }

// fakeRejection maps the fake chain's refusals. Its Class values are the
// shared vocabulary, so they pass through unchanged, including the
// gouroboros-prefixed ones that have no cardano-ledger name.
func fakeRejection(err error) ([]Failure, bool) {
	var re *fakechain.RejectError
	if errors.As(err, &re) {
		fs := make([]Failure, len(re.Failures))
		for i, f := range re.Failures {
			fs[i] = Failure{Class: Class(f.Class)}
			if f.Err != nil {
				fs[i].Detail = f.Err.Error()
			}
		}
		return fs, true
	}
	if errors.Is(err, fakechain.ErrDecode) {
		return printable([]Failure{{Class: Undecodable, Detail: err.Error()}}), true
	}
	return nil, false
}
