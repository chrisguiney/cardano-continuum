package protocol

import "testing"

func TestParseSchedule(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want Schedule
	}{
		{"11.0@epoch:3", Schedule{To: Version{11, 0}, Epoch: 3}},
		{"11.0", Schedule{To: Version{11, 0}, Epoch: DefaultForkEpoch}},
		{"10.1@epoch:0", Schedule{To: Version{10, 1}}},
		{"none", Schedule{}},
		{"", Schedule{}},
	} {
		got, err := ParseSchedule(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseSchedule(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}

	for _, s := range []string{"11", "11.0@3", "11.0@epoch:", "11.0@epoch:-1", "11.0@epoch:x", "@epoch:3", "none@epoch:3", "11.0@slot:3", "11.0@epoch:3@epoch:4"} {
		if _, err := ParseSchedule(s); err == nil {
			t.Errorf("ParseSchedule(%q) accepted", s)
		}
	}
}

func TestScheduleString(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"11.0@epoch:3": "11.0@epoch:3", "11.0": "11.0@epoch:3", "none": "none", "": "none"} {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Fatal(err)
		}

		if got := s.String(); got != want {
			t.Errorf("ParseSchedule(%q).String() = %q, want %q", in, got, want)
		}

		if back, err := ParseSchedule(s.String()); err != nil || back != s {
			t.Errorf("%q does not round-trip: %+v, %v", s.String(), back, err)
		}
	}
}

func TestDefaultSchedule(t *testing.T) {
	t.Parallel()
	if got := DefaultSchedule().String(); got != "11.0@epoch:3" {
		t.Fatalf("default schedule %q", got)
	}
}

func TestScheduleDue(t *testing.T) {
	t.Parallel()
	s := Schedule{To: Version{11, 0}, Epoch: 3}
	for _, c := range []struct {
		epoch uint64
		cur   Version
		want  bool
	}{
		{2, Version{10, 0}, false},
		{3, Version{10, 0}, true},
		{9, Version{10, 0}, true},
		{9, Version{11, 0}, false},
		{9, Version{12, 0}, false},
	} {
		if got := s.Due(c.epoch, c.cur); got != c.want {
			t.Errorf("Due(%d, %v) = %v, want %v", c.epoch, c.cur, got, c.want)
		}
	}

	if (Schedule{}).Due(9, Version{10, 0}) || (Schedule{}).On() {
		t.Error("the zero schedule is on")
	}
}
