package contract

import (
	"bytes"
	"math/big"

	"github.com/blinklabs-io/plutigo/data"
)

// The datums and redeemers of contracts/templates/lib/continuum/types.ak.
// Aiken encodes a record as Constr 0 with its fields in declaration order
// and an enum variant as Constr <index> with no fields.

// Vesting is a vesting position (VestingDatum).
type Vesting struct {
	// Owner is the payment key hash that may claim.
	Owner []byte
	// Until is the POSIX time in milliseconds the claim opens at.
	Until int64
}

// Data encodes v as the validator reads it.
func (v Vesting) Data() data.PlutusData {
	return data.NewConstr(0, data.NewByteString(bytes.Clone(v.Owner)), data.NewInteger(big.NewInt(v.Until)))
}

// ParseVesting decodes a VestingDatum.
func ParseVesting(d data.PlutusData) (Vesting, bool) {
	fs, ok := record(d, 2)
	if !ok {
		return Vesting{}, false
	}

	owner, ok := fs[0].(*data.ByteString)
	until, isInt := fs[1].(*data.Integer)
	if !ok || !isInt || !until.Inner.IsInt64() {
		return Vesting{}, false
	}
	return Vesting{Owner: bytes.Clone(owner.Inner), Until: until.Inner.Int64()}, true
}

// Escrow is an escrow's parties (EscrowDatum).
type Escrow struct {
	// Depositor is the payment key hash that releases.
	Depositor []byte
	// Beneficiary is the payment key hash released to, which may refund.
	Beneficiary []byte
}

// Data encodes e as the validator reads it.
func (e Escrow) Data() data.PlutusData {
	return data.NewConstr(0, data.NewByteString(bytes.Clone(e.Depositor)), data.NewByteString(bytes.Clone(e.Beneficiary)))
}

// ParseEscrow decodes an EscrowDatum.
func ParseEscrow(d data.PlutusData) (Escrow, bool) {
	fs, ok := record(d, 2)
	if !ok {
		return Escrow{}, false
	}

	dep, ok := fs[0].(*data.ByteString)
	ben, isBytes := fs[1].(*data.ByteString)
	if !ok || !isBytes {
		return Escrow{}, false
	}
	return Escrow{Depositor: bytes.Clone(dep.Inner), Beneficiary: bytes.Clone(ben.Inner)}, true
}

// Owner is the key hash in a template datum's first field: a vesting
// owner or an escrow depositor, the actor that locked the output.
func Owner(d data.PlutusData) ([]byte, bool) {
	fs, ok := record(d, 2)
	if !ok {
		return nil, false
	}

	b, ok := fs[0].(*data.ByteString)
	if !ok {
		return nil, false
	}
	return bytes.Clone(b.Inner), true
}

// record returns the fields of d when it is Constr 0 with n fields.
func record(d data.PlutusData, n int) ([]data.PlutusData, bool) {
	c, ok := d.(*data.Constr)
	if !ok || c.Tag.Sign() != 0 {
		return nil, false
	}

	fs := c.Fields()
	if len(fs) != n {
		return nil, false
	}
	return fs, true
}

// EscrowAction is what an escrow spend does (EscrowAction).
type EscrowAction uint8

// The escrow actions, in constructor order.
const (
	// Release pays the beneficiary, signed by the depositor.
	Release EscrowAction = iota
	// Refund gives the deposit up, signed by the beneficiary.
	Refund
)

// Data encodes a as the validator reads it.
func (a EscrowAction) Data() data.PlutusData { return data.NewConstr(uint64(a)) }

// String names the action.
func (a EscrowAction) String() string {
	if a == Refund {
		return "refund"
	}
	return "release"
}
