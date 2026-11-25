//go:build debug

package check

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

// Slow runs fn and panics when it reports false. fn is called only in debug
// builds, so it may scan whole structures.
func Slow(name string, fn func() bool) {
	if !fn() {
		panic("check: invariant: " + name)
	}
}
