package image

import (
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRegistryPrecedence(t *testing.T) {
	t.Parallel()
	e := env(map[string]string{EnvRegistry: "env.example:5000/c"})
	tests := []struct {
		name, flag string
		env        func(string) string
		def, want  string
	}{
		{"flag beats env", "flag.example/c", e, "mk.example/c", "flag.example/c"},
		{"env beats default", "", e, "mk.example/c", "env.example:5000/c"},
		{"default", "", env(nil), "mk.example/c", "mk.example/c"},
		{"unset", "", env(nil), "", ""},
	}
	for _, tt := range tests {
		if got := Registry(tt.flag, tt.env, tt.def); got != tt.want {
			t.Errorf("%s: Registry = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestPushNeedsRegistry(t *testing.T) {
	t.Parallel()
	if _, err := NewSettings("", nil, env(nil), ""); err != nil {
		t.Fatalf("no push, no registry: %v", err)
	}
	_, err := NewSettings("", nil, env(nil), "", WithPush())
	if err == nil || !strings.Contains(err.Error(), EnvRegistry) {
		t.Fatalf("push without registry: err = %v, want mention of %s", err, EnvRegistry)
	}
}

func TestInsecureSettingFansOut(t *testing.T) {
	t.Parallel()
	on := env(map[string]string{EnvRegistry: "localhost:5000/continuum", EnvInsecure: "true"})
	s, err := NewSettings("", nil, on, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.KoFlags(), []string{"--insecure-registry"}; !reflect.DeepEqual(got, want) {
		t.Errorf("KoFlags = %v, want %v", got, want)
	}
	if got, want := s.Resolver().InsecureRegistries, []string{"localhost:5000"}; !reflect.DeepEqual(got, want) {
		t.Errorf("InsecureRegistries = %v, want %v", got, want)
	}
	if got := s.DaemonHost(); got != "localhost:5000" {
		t.Errorf("DaemonHost = %q", got)
	}

	off := env(map[string]string{EnvRegistry: "localhost:5000/continuum"})
	s, err = NewSettings("", nil, off, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.KoFlags()) != 0 || len(s.Resolver().InsecureRegistries) != 0 || s.DaemonHost() != "" {
		t.Errorf("unset must fan out nothing: %v %v %q", s.KoFlags(), s.Resolver(), s.DaemonHost())
	}

	// The flag beats the environment.
	f := true
	s, err = NewSettings("", &f, off, "")
	if err != nil || !s.Insecure {
		t.Fatalf("flag true: %v %v", s.Insecure, err)
	}
	f = false
	s, err = NewSettings("", &f, on, "")
	if err != nil || s.Insecure {
		t.Fatalf("flag false: %v %v", s.Insecure, err)
	}
}

func TestInsecureRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, err := NewSettings("", nil, env(map[string]string{EnvInsecure: "maybe"}), "")
	if err == nil {
		t.Fatal("want error for unparsable CONTINUUM_REGISTRY_INSECURE")
	}
}
