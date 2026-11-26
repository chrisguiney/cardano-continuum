package scenario

import "encoding/binary"

// CBOR major types (RFC 8949 section 3.1).
const (
	majUint  = 0
	majNint  = 1
	majBytes = 2
	majArray = 4
	majMap   = 5
	majTag   = 6
)

// The crafter writes CBOR by hand rather than through an encoder: a scenario
// decides every byte, including the forms an encoder never emits (a length in
// a wider head than it needs, an indefinite container, a bignum where an
// integer belongs).

// head writes a major type with its argument in the shortest form.
func head(major byte, arg uint64) []byte {
	switch {
	case arg < 24:
		return []byte{major<<5 | byte(arg)}
	case arg <= 0xff:
		return []byte{major<<5 | 24, byte(arg)}
	case arg <= 0xffff:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(arg))
	case arg <= 0xffffffff:
		return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(arg))
	}
	return binary.BigEndian.AppendUint64([]byte{major<<5 | 27}, arg)
}

// wideHead writes the argument in a one-byte extension even when it fits in
// the initial byte: well-formed, not the shortest form.
func wideHead(major byte, arg uint8) []byte { return []byte{major<<5 | 24, arg} }

func cUint(v uint64) []byte { return head(majUint, v) }

func cInt(v int64) []byte {
	if v >= 0 {
		return head(majUint, uint64(v))
	}
	return head(majNint, uint64(-(v + 1)))
}

func cBytes(b []byte) []byte { return append(head(majBytes, uint64(len(b))), b...) }

func cTag(n uint64, item []byte) []byte { return append(head(majTag, n), item...) }

func cArray(items ...[]byte) []byte {
	out := head(majArray, uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// cIndefArray writes the items between an indefinite-length head and a break.
func cIndefArray(items ...[]byte) []byte {
	out := []byte{majArray<<5 | 31}
	for _, it := range items {
		out = append(out, it...)
	}
	return append(out, 0xff)
}

// cMap writes key, value, key, value, ... in the order given.
func cMap(kv ...[]byte) []byte {
	out := head(majMap, uint64(len(kv)/2))
	for _, x := range kv {
		out = append(out, x...)
	}
	return out
}

// cSet writes a Conway set: the items under tag 258.
func cSet(items ...[]byte) []byte { return cTag(258, cArray(items...)) }

var (
	cTrue  = []byte{0xf5}
	cFalse = []byte{0xf4}
	cNull  = []byte{0xf6}
)

func cBool(b bool) []byte {
	if b {
		return cTrue
	}
	return cFalse
}
