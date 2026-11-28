package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultForkEpoch is the epoch a schedule without one starts at: late
// enough for the pools to hold stake and the committee to hold hot keys.
const DefaultForkEpoch = 3

// Schedule is the hard fork a run proposes by itself: the version it moves
// to and the first epoch at which it is proposed, if the network is ready
// then. The zero value schedules nothing.
type Schedule struct {
	// To is the version to move to; the zero version means no fork.
	To Version `json:"to"`
	// Epoch is the first epoch the fork may be proposed in.
	Epoch uint64 `json:"epoch"`
}

// ParseSchedule reads "none" (or empty), "<major.minor>" or
// "<major.minor>@epoch:<n>". A version alone starts at DefaultForkEpoch.
func ParseSchedule(s string) (Schedule, error) {
	if s == "" || s == "none" {
		return Schedule{}, nil
	}

	ver, at, has := strings.Cut(s, "@")
	to, err := Parse(ver)
	if err != nil {
		return Schedule{}, fmt.Errorf("hard fork %q: %w", s, err)
	}

	if !has {
		return Schedule{To: to, Epoch: DefaultForkEpoch}, nil
	}

	n, ok := strings.CutPrefix(at, "epoch:")
	if !ok {
		return Schedule{}, fmt.Errorf("hard fork %q: want <major.minor>@epoch:<n>", s)
	}

	ep, err := strconv.ParseUint(n, 10, 32)
	if err != nil {
		return Schedule{}, fmt.Errorf("hard fork %q: epoch: %w", s, err)
	}
	return Schedule{To: to, Epoch: ep}, nil
}

// On reports whether the schedule names a fork.
func (s Schedule) On() bool { return s.To != Version{} }

// String is the form ParseSchedule reads.
func (s Schedule) String() string {
	if !s.On() {
		return "none"
	}
	return s.To.String() + "@epoch:" + strconv.FormatUint(s.Epoch, 10)
}

// Due reports whether the fork may be proposed in epoch with cur in
// force: the epoch has come and the version is not yet reached.
func (s Schedule) Due(epoch uint64, cur Version) bool {
	return s.On() && epoch >= s.Epoch && cur.Less(s.To)
}
