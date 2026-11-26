package tla

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	// maxDepth bounds nesting so a hostile document cannot exhaust the stack.
	maxDepth = 64
	// maxBytes bounds one decoded document.
	maxBytes = 64 << 20
)

// Value is a decoded TLA+ value. TLC's JSON writer maps records and string-keyed
// functions to objects, sequences, tuples and sets to arrays, strings to
// strings, booleans to booleans and integers to numbers, so a set and a
// sequence cannot be told apart once decoded. The Go types are:
//
//	map[string]Value  record or function
//	[]Value           sequence, tuple or set
//	string            string or model value
//	bool              boolean
//	int64             integer that fits
//	*big.Int          integer that does not
type Value = any

// DecodeValue reads one JSON document in TLC's value encoding. It rejects
// null, non-integer numbers, trailing data and nesting deeper than 64.
func DecodeValue(r io.Reader) (Value, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read value: %w", err)
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("value larger than %d MiB", maxBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc Value
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode value: trailing data")
	}
	return convert(doc, 0)
}

func convert(v Value, depth int) (Value, error) {
	if depth >= maxDepth {
		return nil, fmt.Errorf("value nested deeper than %d", maxDepth)
	}
	switch x := v.(type) {
	case string, bool:
		return x, nil
	case json.Number:
		return integer(x)
	case []any:
		out := make([]Value, len(x))
		for i, e := range x {
			c, err := convert(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case map[string]any:
		out := make(map[string]Value, len(x))
		for k, e := range x {
			c, err := convert(e, depth+1)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", k, err)
			}
			out[k] = c
		}
		return out, nil
	}
	return nil, errors.New("unsupported JSON null")
}

func integer(n json.Number) (Value, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return nil, fmt.Errorf("non-integer number %s", s)
	}
	if i, err := n.Int64(); err == nil {
		return i, nil
	}
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("bad integer %s", s)
	}
	return b, nil
}
