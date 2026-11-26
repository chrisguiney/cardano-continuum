package tla

import (
	"os"
	"strings"
	"testing"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestParseTraceHello(t *testing.T) {
	tr, err := ParseTrace(openFixture(t, "hello.trace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(tr.Vars, ","), "state,pings,pongs"; got != want {
		t.Fatalf("vars = %s, want %s", got, want)
	}
	wantActions := []string{"Initial", "SendPing", "SendPong", "SendDone"}
	if len(tr.Steps) != len(wantActions) {
		t.Fatalf("steps = %d, want %d", len(tr.Steps), len(wantActions))
	}
	for i, s := range tr.Steps {
		if s.Num != i+1 || s.Action != wantActions[i] {
			t.Errorf("step %d = %d %q, want %d %q", i, s.Num, s.Action, i+1, wantActions[i])
		}
	}
	last := tr.Steps[3].State
	if last["state"] != "Done" || last["pings"] != int64(1) || last["pongs"] != int64(1) {
		t.Fatalf("last state = %#v", last)
	}
}

func TestParseTraceKeepAlive(t *testing.T) {
	tr, err := ParseTrace(openFixture(t, "keepalive.trace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) < 3 {
		t.Fatalf("steps = %d", len(tr.Steps))
	}
	if tr.Steps[1].Action != "ClientDone" {
		t.Fatalf("step 2 action = %q", tr.Steps[1].Action)
	}
	var ping *Step
	for i := range tr.Steps {
		if tr.Steps[i].Action == "ClientPing" {
			ping = &tr.Steps[i]
		}
	}
	if ping == nil || len(ping.Params) != 1 || ping.Params[0] != "c" {
		t.Fatalf("ClientPing params = %+v", ping)
	}
	inbox, ok := tr.Steps[1].State["inbox"].(map[string]Value)
	if !ok {
		t.Fatalf("inbox = %#v", tr.Steps[1].State["inbox"])
	}
	if srv, ok := inbox["Server"].([]Value); !ok || len(srv) != 1 {
		t.Fatalf("server inbox = %#v", inbox["Server"])
	}
}

func TestParseTraceRejects(t *testing.T) {
	tests := map[string]string{
		"not json":          `nope`,
		"no counterexample": `{"vars":[]}`,
		"no states":         `{"counterexample":{"state":[]},"vars":[]}`,
		"bad state":         `{"counterexample":{"state":[[1,"x"]]},"vars":[]}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTrace(strings.NewReader(in)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func FuzzParseTrace(f *testing.F) {
	for _, n := range []string{"hello.trace.json", "keepalive.trace.json"} {
		b, err := os.ReadFile("testdata/" + n)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		tr, err := ParseTrace(strings.NewReader(string(b)))
		if err == nil && len(tr.Steps) == 0 {
			t.Fatal("no steps without error")
		}
	})
}
