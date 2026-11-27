package eventlog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeDeterministic(t *testing.T) {
	a := map[string]int{"a": 1, "b": 2, "c": 3}
	b := map[string]int{"c": 3, "b": 2, "a": 1}
	ea, err := Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	eb, _ := Encode(b)
	if !bytes.Equal(ea, eb) {
		t.Fatalf("insertion order changed bytes: %s vs %s", ea, eb)
	}
	if want := `{"a":1,"b":2,"c":3}`; string(ea) != want {
		t.Fatalf("golden: got %s want %s", ea, want)
	}
}

// TestEncodeRefusesNonFinite: JSON has no NaN or infinity, and a body that
// silently lost one would replay as a different decision.
func TestEncodeRefusesNonFinite(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if b, err := Encode(struct{ F float64 }{f}); err == nil {
			t.Fatalf("%v encoded as %s", f, b)
		}
	}
}

func TestEncodeForms(t *testing.T) {
	type body struct {
		D    time.Duration
		Hash Hex
		Raw  []byte
		Nil  []uint64
		F    float64
	}
	got, err := Encode(body{D: 1500 * time.Millisecond, Hash: Hex{0xab, 0x01}, Raw: []byte{1, 2, 3}, F: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"D":1500000000,"Hash":"ab01","Raw":"AQID","Nil":null,"F":0.1}`; string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	var back body
	if err := Decode(got, &back); err != nil {
		t.Fatal(err)
	}
	if back.D != 1500*time.Millisecond || !bytes.Equal(back.Hash, []byte{0xab, 0x01}) || back.Nil != nil || back.F != 0.1 {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestDecodeBounds(t *testing.T) {
	var v struct{ A int }
	for name, in := range map[string]string{
		"unknown member":  `{"A":1,"B":2}`,
		"duplicate name":  `{"A":1,"A":2}`,
		"trailing data":   `{"A":1} {}`,
		"too deep":        strings.Repeat("[", maxDepth+1) + strings.Repeat("]", maxDepth+1),
		"not json at all": "\xa1\x01\x02",
	} {
		if err := Decode([]byte(in), &v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var deep any
	ok := strings.Repeat("[", maxDepth) + strings.Repeat("]", maxDepth)
	if err := Decode([]byte(ok), &deep); err != nil {
		t.Fatalf("depth %d refused: %v", maxDepth, err)
	}
}

// TestFrameCarriesBodyAsJSON: the payload is one JSON object and the body
// sits in it as JSON, not as an encoded string.
func TestFrameCarriesBodyAsJSON(t *testing.T) {
	r := Record{Seq: 4, At: 9, Class: Decision, Kind: "X", Actor: 2, Body: []byte(`{"a":1}`)}
	f, err := AppendFrame(nil, r)
	if err != nil {
		t.Fatal(err)
	}
	if f[frameHead] != FormatVersion {
		t.Fatalf("version byte %d", f[frameHead])
	}
	want := `{"seq":4,"at":9,"class":2,"kind":"X","actor":2,"body":{"a":1}}`
	if got := string(f[frameHead+1:]); got != want {
		t.Fatalf("payload %s, want %s", got, want)
	}
	back, n, err := DecodeFrame(f)
	if err != nil || n != len(f) || string(back.Body) != `{"a":1}` || back.Seq != 4 || back.Actor != 2 {
		t.Fatalf("decode: %+v %d %v", back, n, err)
	}
	if _, err := AppendFrame(nil, Record{Seq: 1, Body: []byte{1, 2}}); err == nil {
		t.Fatal("a body that is not JSON was framed")
	}
	empty, err := AppendFrame(nil, Record{Seq: 1, Kind: "K"})
	if err != nil {
		t.Fatal(err)
	}
	if back, _, err := DecodeFrame(empty); err != nil || back.Body != nil {
		t.Fatalf("empty body: %+v %v", back, err)
	}
}

// oldFrame is a frame as the CBOR build wrote it: version byte 1.
func oldFrame(payload []byte) []byte {
	body := append([]byte{1}, payload...)
	f := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	f = binary.BigEndian.AppendUint32(f, crc32.Checksum(body, crcTable))
	return append(f, body...)
}

func TestOldFormatRefused(t *testing.T) {
	// A CBOR six-element array: what a version 1 payload starts with.
	f := oldFrame([]byte{0x86, 0x01, 0x00, 0x03, 0x66})
	if _, _, err := DecodeFrame(f); !errors.Is(err, ErrOldFormat) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("DecodeFrame: err = %v, want ErrOldFormat and not ErrCorrupt", err)
	}
	// Open must refuse the file, not cut the old frame off as a torn tail:
	// the frame is the whole file, so it is also the final frame.
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, f, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); !errors.Is(err, ErrOldFormat) {
		t.Fatalf("Open: err = %v, want ErrOldFormat", err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() != int64(len(f)) {
		t.Fatalf("old log changed: %v %v", st, err)
	}
}

// TestLeaksSecretSeesEncodings: a body carries bytes as base64 or hex, so
// the scan must look for those forms as well as the raw bytes.
func TestLeaksSecretSeesEncodings(t *testing.T) {
	secret := bytes.Repeat([]byte{0xA5, 0x5A, 0xC3}, 11)
	for name, body := range map[string]any{
		"base64": struct{ K []byte }{secret},
		"hex":    struct{ K Hex }{secret},
		"text":   struct{ K string }{hex.EncodeToString(secret)},
		"b64str": struct{ K string }{base64.StdEncoding.EncodeToString(secret)},
	} {
		b, err := Encode(body)
		if err != nil {
			t.Fatal(err)
		}
		log := &MemLog{}
		if err := log.Append(context.Background(), Record{Class: Input, Kind: "Control", Body: b}); err != nil {
			t.Fatal(err)
		}
		if seq, err := LeaksSecret(context.Background(), log, secret); err != nil || seq != 1 {
			t.Errorf("%s: seq %d err %v", name, seq, err)
		}
	}
}
