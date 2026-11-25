//go:build debug

package check

// Slow is true in debug builds. Wrap any check that iterates, allocates or
// calls a method in `if check.Slow { ... }`; release builds delete the block.
const Slow = true

// Pre panics when a precondition does not hold. msg names the contract.
func Pre(ok bool, msg string) {
	if !ok {
		panic("check: precondition: " + msg)
	}
}

// Post panics when a postcondition does not hold. msg names the contract.
func Post(ok bool, msg string) {
	if !ok {
		panic("check: postcondition: " + msg)
	}
}

// PreFn panics when fn reports false. fn is called only in debug builds, so
// it may scan whole structures.
func PreFn(fn func() bool, msg string) { Pre(fn(), msg) }

// PostFn is PreFn for a postcondition.
func PostFn(fn func() bool, msg string) { Post(fn(), msg) }
