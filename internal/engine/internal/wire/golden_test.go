package wire

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

var update = flag.Bool("update", false, "rewrite testdata/bodies/*.json")

// bodies are one zero value per log body type, by record kind; the snapshot
// records an intent as Pending.
func bodies() map[string]any {
	return map[string]any{
		KindSpawned:        &Spawned{},
		KindTransition:     &Transition{},
		KindIntentChosen:   &agenda.Decision{},
		KindSubmitted:      &Submitted{},
		KindSkipped:        &Skipped{},
		KindReleased:       &Released{},
		KindConfirmed:      &Confirmed{},
		KindRefused:        &Refused{},
		KindGaveUp:         &GaveUp{},
		KindRefill:         &Refill{},
		KindGuarded:        &Guarded{},
		KindPoolTier:       &PoolTier{},
		KindRepair:         &Repair{},
		KindIncident:       &Incident{},
		KindControlled:     &Controlled{},
		KindImageChange:    &ImageChange{},
		KindDeployed:       &Deployed{},
		KindQuote:          &Quote{},
		KindAttack:         &Attack{},
		KindProtocol:       &Protocol{},
		oracle.KindFinding: &oracle.Finding{},

		eventlog.KindHeader:          &eventlog.HeaderBody{},
		eventlog.KindBlockApplied:    &BlockContent{},
		eventlog.KindRolledBack:      &eventlog.RolledBack{},
		eventlog.KindSubmitResult:    &SubmitResult{},
		eventlog.KindPoolStatus:      &PoolStatus{},
		eventlog.KindControl:         &Control{},
		eventlog.KindMonitorSnapshot: &MonitorSnapshot{},
		"Pending":                    &Pending{},
	}
}

// TestBodyGoldens pins every body's bytes. Each body has every field set
// by fill, so renaming, retagging or reordering a field changes a golden:
// that is a log format change and needs logFormat (or snapshotVersion)
// bumped. Regenerate deliberately with
// go test ./internal/engine/internal/wire -run TestBodyGoldens -update
func TestBodyGoldens(t *testing.T) {
	for kind, v := range bodies() {
		t.Run(kind, func(t *testing.T) {
			n := 0
			fill(reflect.ValueOf(v).Elem(), &n)
			got, err := eventlog.Encode(v)
			if err != nil {
				t.Fatal(err)
			}

			back := reflect.New(reflect.TypeOf(v).Elem())
			if err := eventlog.Decode(got, back.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back.Interface(), v) {
				t.Fatalf("round trip\n got %+v\nwant %+v", back.Interface(), v)
			}

			path := filepath.Join("testdata", "bodies", kind+".json")
			if *update {
				if err := os.WriteFile(path, append(got, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(append(got, '\n'), want) {
				t.Fatalf("%s changed:\n got %s\nwant %s", path, got, want)
			}
		})
	}
}

// TestBodyGoldensCoverKinds: every decision kind has a golden.
func TestBodyGoldensCoverKinds(t *testing.T) {
	b := bodies()
	for _, k := range append(DecisionKinds(), KindAttack) {
		if _, ok := b[k]; !ok {
			t.Errorf("no golden body for %s", k)
		}
	}
	files, err := filepath.Glob(filepath.Join("testdata", "bodies", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(b) {
		t.Fatalf("%d golden files for %d bodies", len(files), len(b))
	}
}

// fill sets every field of v to a value that is not zero and differs from
// its neighbours: n counts the leaves filled so far. Interfaces stay nil;
// no body holds one.
func fill(v reflect.Value, n *int) {
	*n++
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n) + 0.25)
	case reflect.String:
		v.SetString(v.Type().Name() + string(rune('a'+*n%26)))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), n)
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), n)
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, n)
		fill(e, n)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), n)
			}
		}
	}
}
