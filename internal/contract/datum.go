package contract

import (
	"bytes"

	"github.com/blinklabs-io/plutigo/data"
)

// The datums and redeemers of contracts/templates/lib/continuum are
// generated into types_gen.go from the blueprint's definitions
// (RenderTypes). Aiken encodes a record as Constr 0 with its fields in
// declaration order and an enum variant as Constr <index> with no fields;
// the decoders below are what the generated code calls.

// Owner is the key hash in a template datum's first field: a vesting
// owner or an escrow depositor, the actor that locked the output.
func Owner(d data.PlutusData) ([]byte, bool) {
	c, ok := d.(*data.Constr)
	if !ok || c.Tag.Sign() != 0 || len(c.Fields()) == 0 {
		return nil, false
	}
	return bytesField(c.Fields()[0])
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

// enumTag is d's constructor index when d is a constructor without
// fields whose index is below n.
func enumTag(d data.PlutusData, n int) (uint8, bool) {
	c, ok := d.(*data.Constr)
	if !ok || len(c.Fields()) != 0 || !c.Tag.IsUint64() || c.Tag.Uint64() >= uint64(n) {
		return 0, false
	}
	return uint8(c.Tag.Uint64()), true
}

func bytesField(d data.PlutusData) ([]byte, bool) {
	b, ok := d.(*data.ByteString)
	if !ok {
		return nil, false
	}
	return bytes.Clone(b.Inner), true
}

func intField(d data.PlutusData) (int64, bool) {
	i, ok := d.(*data.Integer)
	if !ok || !i.Inner.IsInt64() {
		return 0, false
	}
	return i.Inner.Int64(), true
}
