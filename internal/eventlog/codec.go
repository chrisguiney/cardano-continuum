package eventlog

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"
)

// maxDepth bounds the nesting of a decoded body, so a hostile log cannot
// drive the decoder's stack.
const maxDepth = 16

// The codec's options. Equal values give equal bytes: struct members in
// declaration order (so reordering fields is a format change), map keys
// sorted, floats in their shortest round-trip form, no HTML escaping, no
// whitespace. Nil slices and maps encode as null so they decode back to nil.
// A time.Duration is integer nanoseconds, chosen here rather than left to a
// library default. JSON has no NaN or infinity: encoding one is an error.
var (
	durations = json.JoinOptions(
		json.WithMarshalers(json.MarshalToFunc(func(enc *jsontext.Encoder, d time.Duration) error {
			return enc.WriteToken(jsontext.Int(int64(d)))
		})),
		json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, d *time.Duration) error {
			var n int64
			if err := json.UnmarshalDecode(dec, &n); err != nil {
				return err
			}
			*d = time.Duration(n)
			return nil
		})),
	)
	encOpts = json.JoinOptions(
		json.Deterministic(true),
		jsontext.EscapeForHTML(false),
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
		durations,
	)
	decOpts = json.JoinOptions(
		json.RejectUnknownMembers(true),
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
		durations,
	)
)

// Encode returns the deterministic JSON of v, so equal values give equal
// bytes whatever a map's insertion order. Record bodies are made with it.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v, encOpts)
	if err != nil {
		return nil, fmt.Errorf("eventlog: encode: %w", err)
	}
	return b, nil
}

// Decode parses JSON produced by Encode into v. It refuses unknown members,
// duplicate names, trailing data and nesting deeper than maxDepth.
func Decode(b []byte, v any) error {
	return decode(b, v, maxDepth)
}

func decode(b []byte, v any, depth int) error {
	if err := within(b, depth); err != nil {
		return fmt.Errorf("eventlog: decode: %w", err)
	}
	if err := json.Unmarshal(b, v, decOpts); err != nil {
		return fmt.Errorf("eventlog: decode: %w", err)
	}
	return nil
}

// within checks b nests at most depth deep. It is a linear token scan; the
// standard library's own depth bound is not exported.
func within(b []byte, depth int) error {
	d := jsontext.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := d.ReadToken(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if d.StackDepth() > depth {
			return fmt.Errorf("nested deeper than %d", depth)
		}
	}
}

// Hex is bytes that encode as a lowercase hex string: hashes and other
// identifiers a reader matches against explorers and node logs. Opaque
// bytes stay []byte, which encodes as base64.
type Hex []byte

// MarshalText implements encoding.TextMarshaler.
func (h Hex) MarshalText() ([]byte, error) {
	return hex.AppendEncode(nil, h), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. The empty string is a
// nil Hex, as a nil Hex encodes to it.
func (h *Hex) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*h = nil
		return nil
	}
	out, err := hex.AppendDecode(nil, b)
	if err != nil {
		return fmt.Errorf("hex: %w", err)
	}
	*h = out
	return nil
}
