// Package check provides assertions that run only in debug builds.
//
// Pre and Post guard cheap contracts. Slow guards invariants that scan
// whole structures, so its closure is never called in release builds. In
// release builds every function here is an empty body that the compiler
// inlines away. Use -tags debug to enable the checks.
package check
