package wire

import (
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func TestTxIDIsHex(t *testing.T) {
	r := Ref{Tx: TxID{0xab, 0x01}, Index: 3}
	b, err := eventlog.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"tx":"ab01` + strings.Repeat("0", 60) + `","index":3}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var back Ref
	if err := eventlog.Decode(b, &back); err != nil || back != r {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	for _, bad := range []string{`{"tx":"ab01","index":3}`, `{"tx":"` + strings.Repeat("zz", 32) + `","index":3}`} {
		if err := eventlog.Decode([]byte(bad), &back); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
