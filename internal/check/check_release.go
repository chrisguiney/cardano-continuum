//go:build !debug

package check

// Slow is false outside debug builds, so `if check.Slow` blocks are removed.
const Slow = false

// Pre is a no-op outside debug builds.
func Pre(bool, string) {}

// Post is a no-op outside debug builds.
func Post(bool, string) {}

// PreFn is a no-op outside debug builds and never calls fn.
func PreFn(func() bool, string) {}

// PostFn is a no-op outside debug builds and never calls fn.
func PostFn(func() bool, string) {}
