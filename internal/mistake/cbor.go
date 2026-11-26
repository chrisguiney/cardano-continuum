package mistake

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
)

// Inject edits a transaction at the CBOR level and leaves every part it does
// not touch byte for byte as the builder wrote it: the witnesses' script
// data hash covers the redeemer and datum bytes as encoded, and re-encoding
// through gouroboros' types would not promise the same bytes. Only definite
// lengths are handled; the builder writes nothing else.

var errShape = errors.New("mistake: unexpected transaction shape")

const (
	majorUint  = 0
	majorBytes = 2
	majorArray = 4
	majorMap   = 5
	majorTag   = 6
	setTag     = 258
)

// head reads a definite-length CBOR head: its major type, argument and
// length in bytes.
func head(b []byte) (major byte, arg uint64, n int, err error) {
	if len(b) == 0 {
		return 0, 0, 0, fmt.Errorf("%w: empty item", errShape)
	}
	major, info := b[0]>>5, b[0]&0x1f
	if info < 24 {
		return major, uint64(info), 1, nil
	}
	size := 0
	switch info {
	case 24:
		size = 1
	case 25:
		size = 2
	case 26:
		size = 4
	case 27:
		size = 8
	default:
		return 0, 0, 0, fmt.Errorf("%w: indefinite or reserved length", errShape)
	}
	if len(b) < 1+size {
		return 0, 0, 0, fmt.Errorf("%w: truncated head", errShape)
	}
	var buf [8]byte
	copy(buf[8-size:], b[1:1+size])
	return major, binary.BigEndian.Uint64(buf[:]), 1 + size, nil
}

// writeHead encodes a head in its shortest form.
func writeHead(major byte, arg uint64) []byte {
	m := major << 5
	switch {
	case arg < 24:
		return []byte{m | byte(arg)}
	case arg <= 0xff:
		return []byte{m | 24, byte(arg)}
	case arg <= 0xffff:
		return binary.BigEndian.AppendUint16([]byte{m | 25}, uint16(arg))
	case arg <= 0xffffffff:
		return binary.BigEndian.AppendUint32([]byte{m | 26}, uint32(arg))
	}
	return binary.BigEndian.AppendUint64([]byte{m | 27}, arg)
}

// items splits count consecutive items off b.
func items(b []byte, count uint64) ([]cbor.RawMessage, error) {
	// Every item is at least one byte, which bounds the allocation.
	if count > uint64(len(b)) {
		return nil, fmt.Errorf("%w: %d items in %d bytes", errShape, count, len(b))
	}
	out := make([]cbor.RawMessage, 0, count)
	for range count {
		var raw cbor.RawMessage
		n, err := cbor.Decode(b, &raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errShape, err)
		}
		out = append(out, cbor.RawMessage(b[:n:n]))
		b = b[n:]
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", errShape, len(b))
	}
	return out, nil
}

// container splits a definite array or map: n items for an array, 2n for a
// map, keys and values alternating.
func container(raw []byte, major byte) ([]cbor.RawMessage, error) {
	m, arg, n, err := head(raw)
	if err != nil {
		return nil, err
	}
	if m != major {
		return nil, fmt.Errorf("%w: major type %d, want %d", errShape, m, major)
	}
	if major == majorMap {
		if arg > uint64(len(raw)) {
			return nil, fmt.Errorf("%w: map of %d entries", errShape, arg)
		}
		arg *= 2
	}
	return items(raw[n:], arg)
}

func splitArray(raw []byte) ([]cbor.RawMessage, error) { return container(raw, majorArray) }

func joinArray(xs []cbor.RawMessage) cbor.RawMessage {
	out := writeHead(majorArray, uint64(len(xs)))
	for _, x := range xs {
		out = append(out, x...)
	}
	return out
}

// set is a CBOR set: an array, optionally under tag 258.
type set struct {
	items  []cbor.RawMessage
	tagged bool
}

func splitSet(raw []byte) (set, error) {
	m, arg, n, err := head(raw)
	if err != nil {
		return set{}, err
	}
	if m == majorTag && arg == setTag {
		xs, err := splitArray(raw[n:])
		return set{xs, true}, err
	}
	xs, err := splitArray(raw)
	return set{xs, false}, err
}

func (s set) join() cbor.RawMessage {
	var out []byte
	if s.tagged {
		out = writeHead(majorTag, setTag)
	}
	return append(out, joinArray(s.items)...)
}

// field is one entry of a map keyed by unsigned integers: a transaction
// body, witness set or post-Alonzo output.
type field struct {
	key uint64
	val cbor.RawMessage
}

type fields []field

func splitFields(raw []byte) (fields, error) {
	kv, err := container(raw, majorMap)
	if err != nil {
		return nil, err
	}
	out := make(fields, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, err := getUint(kv[i])
		if err != nil {
			return nil, err
		}
		out = append(out, field{k, kv[i+1]})
	}
	return out, nil
}

func (fs fields) get(k uint64) (cbor.RawMessage, bool) {
	for _, f := range fs {
		if f.key == k {
			return f.val, true
		}
	}
	return nil, false
}

// set replaces the value of k in place, or appends it.
func (fs fields) set(k uint64, v cbor.RawMessage) fields {
	for i, f := range fs {
		if f.key == k {
			fs[i].val = v
			return fs
		}
	}
	return append(fs, field{k, v})
}

func (fs fields) del(k uint64) fields {
	out := fs[:0]
	for _, f := range fs {
		if f.key != k {
			out = append(out, f)
		}
	}
	return out
}

func (fs fields) join() cbor.RawMessage {
	out := writeHead(majorMap, uint64(len(fs)))
	for _, f := range fs {
		out = append(out, writeHead(majorUint, f.key)...)
		out = append(out, f.val...)
	}
	return out
}

func getUint(raw []byte) (uint64, error) {
	m, arg, n, err := head(raw)
	if err != nil {
		return 0, err
	}
	if m != majorUint || n != len(raw) {
		return 0, fmt.Errorf("%w: not an unsigned integer", errShape)
	}
	return arg, nil
}

func encUint(v uint64) cbor.RawMessage { return writeHead(majorUint, v) }

func getBytes(raw []byte) ([]byte, error) {
	m, arg, n, err := head(raw)
	if err != nil {
		return nil, err
	}
	if m != majorBytes || uint64(len(raw)-n) != arg {
		return nil, fmt.Errorf("%w: not a byte string", errShape)
	}
	return raw[n:], nil
}

func encBytes(b []byte) cbor.RawMessage {
	return append(writeHead(majorBytes, uint64(len(b))), b...)
}

// output is a transaction output in either form: the legacy array
// [address, value, ...] or the post-Alonzo map {0: address, 1: value, ...}.
type output struct {
	arr []cbor.RawMessage
	m   fields
}

func splitOutput(raw []byte) (output, error) {
	m, _, _, err := head(raw)
	if err != nil {
		return output{}, err
	}
	if m == majorArray {
		arr, err := splitArray(raw)
		if err == nil && len(arr) < 2 {
			err = fmt.Errorf("%w: output of %d items", errShape, len(arr))
		}
		return output{arr: arr}, err
	}
	fs, err := splitFields(raw)
	if err != nil {
		return output{}, err
	}
	_, okA := fs.get(0)
	_, okV := fs.get(1)
	if !okA || !okV {
		return output{}, fmt.Errorf("%w: output without address or value", errShape)
	}
	return output{m: fs}, nil
}

func (o output) part(i uint64) cbor.RawMessage {
	if o.arr != nil {
		return o.arr[i]
	}
	v, _ := o.m.get(i)
	return v
}

func (o output) with(i uint64, v cbor.RawMessage) output {
	if o.arr != nil {
		o.arr[i] = v
		return o
	}
	o.m = o.m.set(i, v)
	return o
}

func (o output) join() cbor.RawMessage {
	if o.arr != nil {
		return joinArray(o.arr)
	}
	return o.m.join()
}

func (o output) address() ([]byte, error) { return getBytes(o.part(0)) }

// coin is the lovelace of a value: a bare coin or [coin, multiasset].
func (o output) coin() (uint64, error) {
	v := o.part(1)
	if m, _, _, err := head(v); err == nil && m == majorArray {
		xs, err := splitArray(v)
		if err != nil || len(xs) != 2 {
			return 0, fmt.Errorf("%w: value", errShape)
		}
		return getUint(xs[0])
	}
	return getUint(v)
}

func (o output) withCoin(c uint64) output {
	v := o.part(1)
	if m, _, _, err := head(v); err == nil && m == majorArray {
		if xs, err := splitArray(v); err == nil && len(xs) == 2 {
			xs[0] = encUint(c)
			return o.with(1, joinArray(xs))
		}
	}
	return o.with(1, encUint(c))
}
