package adversary

import (
	"errors"
	"fmt"
	"math"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// A minimal CBOR reader and writer (RFC 8949 section 3): enough to walk a
// transaction's items without decoding them, and to write one from raw
// parts, so a mutant keeps every byte it does not edit and a raw
// transaction bypasses the builder and its validation.

const (
	majorUint   = 0
	majorNeg    = 1
	majorBytes  = 2
	majorText   = 3
	majorArray  = 4
	majorMap    = 5
	majorTag    = 6
	majorSimple = 7

	// indefinite is head's argument for additional information 31.
	indefinite = math.MaxUint64
	// maxDepth bounds itemLen's recursion on hostile input.
	maxDepth = 64
)

var errCBOR = errors.New("adversary: malformed CBOR")

// head reads the initial byte and argument of the item at b: its major
// type, argument and how many bytes the head takes.
func head(b []byte) (major byte, arg uint64, n int, err error) {
	if len(b) == 0 {
		return 0, 0, 0, errCBOR
	}
	major, ai := b[0]>>5, b[0]&0x1f
	switch {
	case ai < 24:
		return major, uint64(ai), 1, nil
	case ai == 31:
		return major, indefinite, 1, nil
	case ai > 27:
		return 0, 0, 0, errCBOR
	}
	w := 1 << (ai - 24)
	if len(b) < 1+w {
		return 0, 0, 0, errCBOR
	}
	for _, x := range b[1 : 1+w] {
		arg = arg<<8 | uint64(x)
	}
	return major, arg, 1 + w, nil
}

// writeHead encodes a head in its shortest form.
func writeHead(major byte, arg uint64) []byte {
	m := major << 5
	switch {
	case arg < 24:
		return []byte{m | byte(arg)}
	case arg <= math.MaxUint8:
		return []byte{m | 24, byte(arg)}
	case arg <= math.MaxUint16:
		return []byte{m | 25, byte(arg >> 8), byte(arg)}
	case arg <= math.MaxUint32:
		return []byte{m | 26, byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg)}
	}
	out := []byte{m | 27}
	for i := 7; i >= 0; i-- {
		out = append(out, byte(arg>>(8*i)))
	}
	return out
}

// itemLen is the length of the whole data item at b.
func itemLen(b []byte, depth int) (int, error) {
	if depth > maxDepth {
		return 0, errCBOR
	}
	major, arg, hl, err := head(b)
	if err != nil {
		return 0, err
	}
	switch major {
	case majorUint, majorNeg, majorSimple:
		return hl, nil
	case majorTag:
		n, err := itemLen(b[hl:], depth+1)
		return hl + n, err
	case majorBytes, majorText:
		if arg == indefinite {
			return items(b, hl, indefinite, depth)
		}
		if arg > uint64(len(b)-hl) {
			return 0, errCBOR
		}
		return hl + int(arg), nil
	}
	if arg != indefinite && major == majorMap {
		if arg > uint64(len(b)) {
			return 0, errCBOR
		}
		arg *= 2
	}
	return items(b, hl, arg, depth)
}

// items is the length of a container at b whose head is hl bytes and that
// holds n items, or runs to a break byte when n is indefinite.
func items(b []byte, hl int, n uint64, depth int) (int, error) {
	if n != indefinite && n > uint64(len(b)) {
		return 0, errCBOR
	}
	pos := hl
	for i := uint64(0); n == indefinite || i < n; i++ {
		if n == indefinite && pos < len(b) && b[pos] == 0xff {
			return pos + 1, nil
		}
		l, err := itemLen(b[pos:], depth+1)
		if err != nil {
			return 0, err
		}
		pos += l
	}
	return pos, nil
}

func encBytes(b []byte) []byte { return append(writeHead(majorBytes, uint64(len(b))), b...) }

func encUint(v uint64) []byte { return writeHead(majorUint, v) }

// RawSpendOf is a raw spend: every input, all of it less Fee paid to To.
type RawSpendOf struct {
	Inputs  []Coin
	To      common.Address
	Fee     uint64
	TTL     uint64
	Signers []keys.Signer
}

// RawSpend writes and signs a Conway transaction byte by byte, with no
// builder and no check that the signers own the inputs: the steal
// mutation spends an honest wallet's output with only the adversary's
// signature.
func RawSpend(s RawSpendOf) ([]byte, error) {
	var total uint64
	for _, c := range s.Inputs {
		total += c.Lovelace
	}
	if len(s.Inputs) == 0 || total <= s.Fee {
		return nil, fmt.Errorf("adversary: raw spend of %d lovelace cannot pay fee %d", total, s.Fee)
	}
	addr, err := s.To.Bytes()
	if err != nil {
		return nil, fmt.Errorf("adversary: raw spend address: %w", err)
	}
	body := writeHead(majorMap, 4)
	body = append(body, encUint(0)...)
	body = append(body, writeHead(majorArray, uint64(len(s.Inputs)))...)
	for _, c := range s.Inputs {
		body = append(body, 0x82)
		body = append(body, encBytes(c.Ref.Tx[:])...)
		body = append(body, encUint(uint64(c.Ref.Index))...)
	}
	body = append(body, encUint(1)...)
	body = append(body, 0x81, 0x82)
	body = append(body, encBytes(addr)...)
	body = append(body, encUint(total-s.Fee)...)
	body = append(body, encUint(2)...)
	body = append(body, encUint(s.Fee)...)
	body = append(body, encUint(3)...)
	body = append(body, encUint(s.TTL)...)
	wits := append(writeHead(majorMap, 1), encUint(0)...)
	wits = append(wits, writeHead(majorArray, uint64(len(s.Signers)))...)
	for _, k := range s.Signers {
		w := k.SignTxBody(body)
		wits = append(wits, 0x82)
		wits = append(wits, encBytes(w.VKey)...)
		wits = append(wits, encBytes(w.Signature)...)
	}
	tx := append([]byte{0x84}, body...)
	tx = append(tx, wits...)
	return append(tx, 0xf5, 0xf6), nil
}
