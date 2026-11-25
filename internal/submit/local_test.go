package submit

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/protocol/localtxsubmission"
)

const testMagic = 42

// wireReject is a rejection reason a server sends as the given CBOR bytes,
// as a real node does.
type wireReject struct{ wire []byte }

func (wireReject) Error() string                  { return "wire reject" }
func (w wireReject) MarshalCBOR() ([]byte, error) { return w.wire, nil }

// serve runs an in-process node-to-client server on 127.0.0.1:0 whose
// local-tx-submission answers every transaction with reply (nil accepts).
func serve(t *testing.T, reply func(tx []byte) error) Dialer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conn, err := ouroboros.NewConnection(
				ouroboros.WithConnection(c),
				ouroboros.WithNetworkMagic(testMagic),
				ouroboros.WithServer(true),
				ouroboros.WithLocalTxSubmissionConfig(localtxsubmission.NewConfig(
					localtxsubmission.WithSubmitTxFunc(func(_ localtxsubmission.CallbackContext, m localtxsubmission.MsgSubmitTxTransaction) error {
						b, _ := m.Raw.Content.([]byte)
						return reply(b)
					}))),
			)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() {
				<-conn.ErrorChan() //nolint:errcheck // the value is only a close signal here
				_ = conn.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

func wire(t *testing.T, v any) wireReject {
	t.Helper()
	b, err := cbor.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return wireReject{b}
}

// conwayReject wraps one UTXO predicate payload the way a node reports it.
func conwayReject(utxo ...any) []any {
	return []any{[]any{6, []any{[]any{1, []any{0, utxo}}}}}
}

func TestLocalAccepts(t *testing.T) {
	var got []byte
	l := NewLocal(serve(t, func(tx []byte) error { got = tx; return nil }), testMagic)
	o := Send(context.Background(), l, 6, []byte{0x80})
	if o.Verdict != Accepted || o.Err != nil {
		t.Fatalf("outcome = %+v", o)
	}
	if string(got) != "\x80" {
		t.Fatalf("server saw %x", got)
	}
}

func TestLocalRejectionDecoded(t *testing.T) {
	tests := []struct {
		name  string
		reply wireReject
		want  Class
	}{
		{"fee", wire(t, conwayReject(5, 170_000, 1)), FeeTooSmall},
		{"bad inputs", wire(t, conwayReject(1, []any{})), BadInputs},
		{"value", wire(t, conwayReject(6, 5, 4)), ValueNotConserved},
		{"era mismatch", wire(t, []any{[]any{1, "Shelley"}, []any{0, "Byron"}}), EraMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLocal(serve(t, func([]byte) error { return tt.reply }), testMagic)
			o := Send(context.Background(), l, 6, []byte{0x80})
			if o.Verdict != Rejected || !o.Has(tt.want) {
				t.Fatalf("outcome = %+v, want rejected with %s", o, tt.want)
			}
		})
	}
}

// TestLocalUnknownRejectionKept feeds a reason no decoder maps and requires
// the text to survive on the outcome.
func TestLocalUnknownRejectionKept(t *testing.T) {
	l := NewLocal(serve(t, func([]byte) error { return wire(t, conwayReject(99, 7)) }), testMagic)
	o := Send(context.Background(), l, 6, []byte{0x80})
	if o.Verdict != Rejected || len(o.Failures) != 1 || o.Failures[0].Class != Unknown || o.Failures[0].Detail == "" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestLocalDialFailureIsTransport(t *testing.T) {
	boom := errors.New("no route")
	l := NewLocal(func(context.Context) (net.Conn, error) { return nil, boom }, testMagic)
	o := Send(context.Background(), l, 6, []byte{0x80})
	if o.Verdict != Unreached || !errors.Is(o.Err, boom) || o.Dead() {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestLocalHungServerUnblocksOnCancel(t *testing.T) {
	block := make(chan struct{})
	l := NewLocal(serve(t, func([]byte) error { <-block; return nil }), testMagic)
	t.Cleanup(func() { close(block) })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o := Send(ctx, l, 6, []byte{0x80})
	if o.Verdict != Unreached || o.Dead() {
		t.Fatalf("outcome = %+v", o)
	}
}
