package rerun

import (
	"context"
	"errors"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

type fakeRunner struct {
	got []Request
	err error
}

func (f *fakeRunner) Rerun(_ context.Context, r Request) error {
	f.got = append(f.got, r)
	return f.err
}

var full = Request{Network: "n", Impl: v1alpha1.Dingo, Ref: "x"}

func hdr() eventlog.HeaderBody {
	return eventlog.HeaderBody{SeedFingerprint: []byte{1}, ProfileHash: []byte{2}, GenesisHash: []byte{3}, BinaryVersion: "m1"}
}

func TestMismatchedFingerprintsRefuse(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]func(*eventlog.HeaderBody){
		"seed":    func(h *eventlog.HeaderBody) { h.SeedFingerprint = []byte{9} },
		"profile": func(h *eventlog.HeaderBody) { h.ProfileHash = []byte{9} },
		"genesis": func(h *eventlog.HeaderBody) { h.GenesisHash = []byte{9} },
	} {
		want := hdr()
		mut(&want)
		run := &fakeRunner{}
		err := Replay(context.Background(), hdr(), want, full, run)
		if !errors.Is(err, ErrFingerprint) {
			t.Errorf("%s: err = %v, want ErrFingerprint", name, err)
		}
		if len(run.got) != 0 {
			t.Errorf("%s: the runner ran despite the mismatch", name)
		}
	}
}

func TestMatchingFingerprintsRun(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{}
	req := Request{Network: "n", Impl: v1alpha1.Dingo, Ref: "reg/dingo@sha256:ab", Seed: 7}
	if err := Replay(context.Background(), hdr(), hdr(), req, run); err != nil {
		t.Fatal(err)
	}
	if len(run.got) != 1 || run.got[0] != req {
		t.Fatalf("runner got %+v", run.got)
	}
}

func TestRunnerErrorIsWrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	err := Replay(context.Background(), hdr(), hdr(), full, &fakeRunner{err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusesAnEmptyTarget(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]Request{
		"no network": {Impl: v1alpha1.Dingo, Ref: "x"},
		"no ref":     {Network: "n", Impl: v1alpha1.Dingo},
		"no impl":    {Network: "n", Ref: "x"},
	} {
		if err := Replay(context.Background(), hdr(), hdr(), r, &fakeRunner{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestHeaderOf(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem := &eventlog.MemLog{}
	body, err := eventlog.Encode(hdr())
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Append(ctx, eventlog.Record{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: body}); err != nil {
		t.Fatal(err)
	}
	got, err := HeaderOf(ctx, mem)
	if err != nil || Check(got, hdr()) != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := HeaderOf(ctx, &eventlog.MemLog{}); err == nil {
		t.Fatal("an empty log has no header")
	}
}
