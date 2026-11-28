package contract

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"
)

// Apply applies v to one Data argument per parameter and returns the
// applied script in witness-set form (CBOR-wrapped flat). Aiken compiles a
// parameterised validator to a program taking each parameter as Data
// before its handler arguments, so applying is wrapping the program's term
// in (con data ...) applications, as `aiken blueprint apply` does. With no
// parameters it returns v's code unchanged.
func Apply(v Validator, args ...data.PlutusData) ([]byte, error) {
	if len(args) != len(v.Params) {
		return nil, fmt.Errorf("contract: %s takes %d parameters, got %d", v.Name, len(v.Params), len(args))
	}

	if len(args) == 0 {
		return append([]byte(nil), v.Code...), nil
	}

	var flat []byte
	if _, err := cbor.Decode(v.Code, &flat); err != nil {
		return nil, fmt.Errorf("contract: %s: unwrap: %w", v.Name, err)
	}

	prog, err := syn.Decode[syn.DeBruijn](flat)
	if err != nil {
		return nil, fmt.Errorf("contract: %s: decode: %w", v.Name, err)
	}

	term := prog.Term
	for _, a := range args {
		term = &syn.Apply[syn.DeBruijn]{
			Function: term,
			Argument: &syn.Constant{Con: &syn.Data{Inner: a}},
		}
	}

	applied, err := syn.Encode(&syn.Program[syn.DeBruijn]{
		Version: prog.Version,
		Term:    term,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: %s: encode: %w", v.Name, err)
	}

	code, err := cbor.Encode(applied)
	if err != nil {
		return nil, fmt.Errorf("contract: %s: wrap: %w", v.Name, err)
	}
	return code, nil
}
