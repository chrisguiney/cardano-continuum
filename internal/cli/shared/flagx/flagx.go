// Package flagx holds the flag helpers several subcommands share.
package flagx

import (
	"flag"
	"strings"
)

// ListFlag collects every occurrence of a repeatable string flag.
type ListFlag []string

func (l *ListFlag) String() string     { return strings.Join(*l, ",") }
func (l *ListFlag) Set(v string) error { *l = append(*l, v); return nil }

// RepoOf drops the tag from ref. The tag is after the last colon only when
// that colon follows the last slash; an earlier one is a registry port.
func RepoOf(ref string) string {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}

// ParseInterleaved parses flags that may come before or after positional
// arguments, which the flag package alone stops at.
func ParseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
