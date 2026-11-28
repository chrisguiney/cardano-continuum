package protocol

import "testing"

// CanFollow is pvCanFollow of the formal ledger spec: the next major
// version with minor zero, or the next minor of the same major; nothing
// else, not even the version itself.
func TestCanFollow(t *testing.T) {
	t.Parallel()
	from := Version{Major: 10, Minor: 2}
	for _, c := range []struct {
		to   Version
		want bool
	}{
		{Version{11, 0}, true},
		{Version{10, 3}, true},
		{Version{11, 1}, false},
		{Version{12, 0}, false},
		{Version{10, 2}, false},
		{Version{10, 4}, false},
		{Version{9, 0}, false},
	} {
		if got := CanFollow(c.to, from); got != c.want {
			t.Errorf("CanFollow(%v, %v) = %v, want %v", c.to, from, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	if v, err := Parse("11.0"); err != nil || v != (Version{11, 0}) {
		t.Fatalf("Parse(11.0) = %v, %v", v, err)
	}
	for _, s := range []string{"", "11", "11.", ".0", "a.b", "11.0.1", "-1.0"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
	if s := (Version{11, 0}).String(); s != "11.0" {
		t.Fatalf("String = %q", s)
	}
	if (Version{10, 0}).Next() != (Version{11, 0}) {
		t.Fatal("Next of 10.0 is not 11.0")
	}
}
