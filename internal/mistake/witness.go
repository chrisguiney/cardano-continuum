package mistake

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
)

// redeemer is one redeemer's editable parts.
type redeemer struct {
	data       cbor.RawMessage
	mem, steps uint64
}

func splitExUnits(raw []byte) (mem, steps uint64, err error) {
	xs, err := splitArray(raw)
	if err != nil || len(xs) != 2 {
		return 0, 0, fmt.Errorf("%w: execution units", errShape)
	}
	if mem, err = getUint(xs[0]); err != nil {
		return 0, 0, err
	}
	steps, err = getUint(xs[1])
	return mem, steps, err
}

func joinExUnits(mem, steps uint64) cbor.RawMessage {
	return joinArray([]cbor.RawMessage{encUint(mem), encUint(steps)})
}

// editPair edits one [data, ex_units].
func editPair(raw []byte, edit func(*redeemer)) (cbor.RawMessage, error) {
	xs, err := splitArray(raw)
	if err != nil || len(xs) != 2 {
		return nil, fmt.Errorf("%w: redeemer value", errShape)
	}
	r := redeemer{data: xs[0]}
	if r.mem, r.steps, err = splitExUnits(xs[1]); err != nil {
		return nil, err
	}
	edit(&r)
	return joinArray([]cbor.RawMessage{r.data, joinExUnits(r.mem, r.steps)}), nil
}
