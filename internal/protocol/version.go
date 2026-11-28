package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// VanRossem is the major version of the Conway intra-era hard fork that
// moves the committee's elected-member check from cardano-node's mempool
// into the ledger's GOV rule (gouroboros common.ProtocolVersionVanRossem).
const VanRossem = 11

// Version is a protocol version (major, minor).
type Version struct {
	// Major changes the ledger rules; a hard fork to a new major is an
	// intra-era or era hard fork.
	Major uint `json:"major"`
	// Minor changes nothing the ledger checks.
	Minor uint `json:"minor"`
}

// String is "major.minor".
func (v Version) String() string {
	return strconv.FormatUint(uint64(v.Major), 10) + "." + strconv.FormatUint(uint64(v.Minor), 10)
}

// Next is the next major version, minor zero: what a hard fork moves to
// by default.
func (v Version) Next() Version { return Version{Major: v.Major + 1} }

// Less orders versions by major, then minor.
func (v Version) Less(o Version) bool {
	return v.Major < o.Major || v.Major == o.Major && v.Minor < o.Minor
}

// CanFollow reports whether a hard fork may move from from to to: the next
// major with minor zero, or the next minor of the same major. It is
// pvCanFollow of formal-ledger-specifications f8059737f1db,
// src/Ledger/Core/Specification/ProtocolVersion.lagda.md (pvCanFollowMajor,
// pvCanFollowMinor), which GOV-Propose applies through validHFAction
// (src/Ledger/Conway/Specification/Gov.lagda.md).
func CanFollow(to, from Version) bool {
	return to == from.Next() || to == Version{Major: from.Major, Minor: from.Minor + 1}
}

// Parse reads "major.minor".
func Parse(s string) (Version, error) {
	maj, min, ok := strings.Cut(s, ".")
	if !ok {
		return Version{}, fmt.Errorf("protocol version %q: want major.minor", s)
	}

	a, err := strconv.ParseUint(maj, 10, 16)
	if err != nil {
		return Version{}, fmt.Errorf("protocol version %q: major: %w", s, err)
	}

	b, err := strconv.ParseUint(min, 10, 16)
	if err != nil {
		return Version{}, fmt.Errorf("protocol version %q: minor: %w", s, err)
	}
	return Version{Major: uint(a), Minor: uint(b)}, nil
}
