// Package check provides assertions that run only in debug builds.
//
// Pre and Post guard cheap contracts; their argument is evaluated in every
// build, so pass only a comparison. PreFn and PostFn take a closure that is
// never called in release builds, for conditions that scan whole structures.
// Slow is a constant for gating several expensive checks at once. Use
// -tags debug to enable the checks.
package check
