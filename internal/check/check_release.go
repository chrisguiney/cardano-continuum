//go:build !debug

package check

// Pre is a no-op outside debug builds.
func Pre(bool, string) {}

// Post is a no-op outside debug builds.
func Post(bool, string) {}

// Slow is a no-op outside debug builds and never calls fn.
func Slow(string, func() bool) {}
