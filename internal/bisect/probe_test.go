package bisect

import (
	"context"
	"errors"
	"testing"
)

type fakeBuilder struct{ fail map[string]bool }

func (b fakeBuilder) Build(_ context.Context, c string) (string, error) {
	if b.fail[c] {
		return "", errors.New("compile error")
	}
	return "img-" + c, nil
}

type checkFn func(ctx context.Context, image, commit string) (Verdict, error)

func (f checkFn) Check(ctx context.Context, image, commit string) (Verdict, error) {
	return f(ctx, image, commit)
}

func TestProberBuildFailureIsSkip(t *testing.T) {
	t.Parallel()
	p := Prober{Builder: fakeBuilder{fail: map[string]bool{"x": true}}, Checker: checkFn(
		func(context.Context, string, string) (Verdict, error) {
			t.Fatal("checked a failed build")
			return Good, nil
		})}
	if v, err := p.Test(context.Background(), "x"); err != nil || v != Skip {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestProberPassesTheImage(t *testing.T) {
	t.Parallel()
	var gotImg string
	p := Prober{Builder: fakeBuilder{}, Checker: checkFn(func(_ context.Context, img, _ string) (Verdict, error) {
		gotImg = img
		return Bad, nil
	})}
	if v, err := p.Test(context.Background(), "abc"); err != nil || v != Bad || gotImg != "img-abc" {
		t.Fatalf("v=%v err=%v img=%q", v, err, gotImg)
	}
}

func TestProberCancelledBuildIsAnError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	b := builderFn(func(context.Context, string) (string, error) { cancel(); return "", ctx.Err() })
	if _, err := (Prober{Builder: b}).Test(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type builderFn func(context.Context, string) (string, error)

func (f builderFn) Build(ctx context.Context, c string) (string, error) { return f(ctx, c) }

func TestKindCheck(t *testing.T) {
	t.Parallel()
	src := func(kinds ...string) FindingSource {
		return func(context.Context, string, string) ([]string, error) { return kinds, nil }
	}
	for name, tc := range map[string]struct {
		kinds []string
		want  Verdict
	}{
		"present": {[]string{"verdict_split", "wrong_class"}, Bad},
		"absent":  {[]string{"verdict_split"}, Good},
		"none":    {nil, Good},
	} {
		v, err := KindCheck{Kind: "wrong_class", Source: src(tc.kinds...)}.Check(context.Background(), "i", "c")
		if err != nil || v != tc.want {
			t.Errorf("%s: v=%v err=%v", name, v, err)
		}
	}
}

func TestCommandCheckExitCodes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		script string
		want   Verdict
	}{
		"zero is good": {"exit 0", Good},
		"one is bad":   {"exit 1", Bad},
		"125 skips":    {"exit 125", Skip},
		"other is bad": {"exit 3", Bad},
	} {
		c := CommandCheck{Argv: []string{"sh", "-c", tc.script}}
		v, err := c.Check(context.Background(), "i", "c")
		if err != nil || v != tc.want {
			t.Errorf("%s: v=%v err=%v", name, v, err)
		}
	}
}

func TestCommandCheckEnvAndMissingBinary(t *testing.T) {
	t.Parallel()
	c := CommandCheck{Argv: []string{"sh", "-c", `[ "$CONTINUUM_IMAGE" = img ] && [ "$CONTINUUM_COMMIT" = abc ] && [ "$CONTINUUM_SEED" = 7 ]`}, Seed: 7}
	if v, err := c.Check(context.Background(), "img", "abc"); err != nil || v != Good {
		t.Fatalf("v=%v err=%v", v, err)
	}
	if _, err := (CommandCheck{Argv: []string{"/no/such/binary"}}).Check(context.Background(), "i", "c"); err == nil {
		t.Fatal("want an error for a command that cannot start")
	}
	if _, err := (CommandCheck{}).Check(context.Background(), "i", "c"); err == nil {
		t.Fatal("want an error for an empty command")
	}
}
