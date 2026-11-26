package adversary

import (
	"errors"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Mutation is one entry of the malformed-transaction corpus. Byte edits
// work on a valid signed transaction and bypass the builder; the semantic
// ones go through internal/mistake; Steal is raw-built (RawSpend).
//
//	mutation         edit                                        a node must answer
//	truncate         drop the last byte                          DeserialiseFailure
//	array-count      the transaction array claims 5 items        DeserialiseFailure
//	body-count       the body map claims one pair more           DeserialiseFailure
//	reserved         additional info 28 in the first head        DeserialiseFailure
//	trailing         one byte after the transaction              DeserialiseFailure
//	non-canonical    the fee re-encoded in 8 bytes, not re-signed  a witness or fee failure
//	missing-signer   mistake.MissingSigner                       MissingVKeyWitnessesUTXOW
//	wrong-network    mistake.WrongNetwork                        WrongNetwork
//	fee-too-small    mistake.FeeTooSmall                         FeeTooSmallUTxO
//	steal            spends an honest wallet's output, signed     MissingVKeyWitnessesUTXOW, or
//	                 only by the adversary                       "inputs spent" if it moved
//
// A node that accepts any of them is the oracle's fatal UnexpectedAccept:
// for a steal, an honest user would lose funds the ledger must protect.
//
// Non-canonical CBOR is legal and cardano-ledger keeps the bytes it was
// given, so the body hash changes and the unchanged signatures no longer
// verify; a node that re-encodes before hashing would accept it.
type Mutation uint8

// The corpus.
const (
	Truncate Mutation = iota + 1
	ArrayCount
	BodyCount
	Reserved
	Trailing
	NonCanonical
	MissingSigner
	WrongNetwork
	FeeTooSmall
	Steal
	numMutations
)

var mutationNames = [numMutations]string{
	Truncate: "truncate", ArrayCount: "array-count", BodyCount: "body-count", Reserved: "reserved",
	Trailing: "trailing", NonCanonical: "non-canonical", MissingSigner: "missing-signer",
	WrongNetwork: "wrong-network", FeeTooSmall: "fee-too-small", Steal: "steal",
}

// StealFee is a raw spend's fee: well over the minimum for its size, so
// the only thing wrong with a steal is the missing witness.
const StealFee = 1_000_000

// Mutations returns the corpus in order.
func Mutations() []Mutation {
	out := make([]Mutation, 0, numMutations-1)
	for m := Truncate; m < numMutations; m++ {
		out = append(out, m)
	}
	return out
}

func (m Mutation) String() string {
	if m > 0 && m < numMutations {
		return mutationNames[m]
	}
	return fmt.Sprintf("Mutation(%d)", m)
}

// raw reports a byte edit Mutate makes.
func (m Mutation) raw() bool { return m >= Truncate && m <= NonCanonical }

// Mistake returns the mistake catalog kinds a semantic mutation is.
func (m Mutation) Mistake() (mistake.KindSet, bool) {
	switch m {
	case MissingSigner:
		return mistake.Of(mistake.MissingSigner), true
	case WrongNetwork:
		return mistake.Of(mistake.WrongNetwork), true
	case FeeTooSmall:
		return mistake.Of(mistake.FeeTooSmall), true
	}
	return 0, false
}

// Expect is what a node must answer to the mutation's first send. Never
// an acceptance.
func Expect(m Mutation) mistake.Expected {
	if ks, ok := m.Mistake(); ok {
		return mistake.Expect(ks, false)
	}
	switch m {
	case NonCanonical:
		// The edit grows the body, so the exact minimum fee no longer
		// covers it either.
		return mistake.Expected{Classes: []submit.Class{submit.FeeTooSmall, submit.InvalidWitnesses, submit.MissingVKeyWitnesses}}
	case Steal:
		// The victim may have spent the output since the adversary saw
		// it; a node that no longer holds it cannot ask for its witness.
		e := mistake.Expect(0, true)
		e.Accept = false
		return withClass(e, submit.MissingVKeyWitnesses)
	}
	return mistake.Expected{Classes: []submit.Class{submit.Undecodable}}
}

func withClass(e mistake.Expected, c submit.Class) mistake.Expected {
	e.Classes = append(slices.Clone(e.Classes), c)
	slices.Sort(e.Classes)
	return e
}

// ErrShape: the bytes are not the Conway transaction shape a byte edit
// expects.
var ErrShape = errors.New("adversary: not a transaction array")

// Mutate makes byte edit m in tx, a Conway transaction. It never panics on
// any input; bytes of the wrong shape are ErrShape.
func Mutate(tx []byte, m Mutation) ([]byte, error) {
	if len(tx) < 2 || tx[0] != 0x84 {
		return nil, ErrShape
	}
	out := append([]byte(nil), tx...)
	switch m {
	case Truncate:
		return out[:len(out)-1], nil
	case ArrayCount:
		out[0] = 0x85
		return out, nil
	case Reserved:
		out[0] = 0x9c
		return out, nil
	case Trailing:
		return append(out, 0x00), nil
	case BodyCount:
		return bodyCount(tx)
	case NonCanonical:
		return widenFee(tx)
	}
	return nil, fmt.Errorf("adversary: %s is not a byte edit", m)
}

// bodyCount rewrites the body map's head with one pair more than it holds.
func bodyCount(tx []byte) ([]byte, error) {
	major, n, hl, err := head(tx[1:])
	if err != nil || major != majorMap || hl == 0 {
		return nil, ErrShape
	}
	out := append([]byte{tx[0]}, writeHead(majorMap, n+1)...)
	return append(out, tx[1+hl:]...), nil
}

// widenFee re-encodes the body's fee (key 2) as an 8-byte unsigned
// integer: the same value, a longer, non-canonical form.
func widenFee(tx []byte) ([]byte, error) {
	at, size, fee, err := findFee(tx[1:])
	if err != nil {
		return nil, err
	}
	at++
	wide := []byte{majorUint<<5 | 27, 0, 0, 0, 0, 0, 0, 0, 0}
	for i := range 8 {
		wide[8-i] = byte(fee >> (8 * i))
	}
	out := append([]byte(nil), tx[:at]...)
	out = append(out, wide...)
	return append(out, tx[at+size:]...), nil
}

// findFee walks the body map at b to key 2 and returns where its value
// starts, how long it is and the fee.
func findFee(b []byte) (at, size int, fee uint64, err error) {
	major, pairs, hl, err := head(b)
	if err != nil || major != majorMap {
		return 0, 0, 0, ErrShape
	}
	pos := hl
	for range pairs {
		kMajor, key, khl, err := head(b[pos:])
		if err != nil {
			return 0, 0, 0, ErrShape
		}
		klen, err := itemLen(b[pos:], 0)
		if err != nil {
			return 0, 0, 0, ErrShape
		}
		vlen, err := itemLen(b[pos+klen:], 0)
		if err != nil {
			return 0, 0, 0, ErrShape
		}
		if kMajor == majorUint && key == 2 && khl == klen {
			vMajor, v, _, err := head(b[pos+klen:])
			if err != nil || vMajor != majorUint {
				return 0, 0, 0, ErrShape
			}
			return pos + klen, vlen, v, nil
		}
		pos += klen + vlen
	}
	return 0, 0, 0, ErrShape
}
