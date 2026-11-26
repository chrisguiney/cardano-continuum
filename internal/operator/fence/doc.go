// Package fence releases the finalizer that keeps a keyed pod object, and so
// the name of its pool's forger, until its process is known to have stopped.
// Releasable is the pure rule; Release is the controller that reads live and
// applies it with a UID- and resourceVersion-preconditioned patch.
//
// See models/specs/pool-forging/README.md ("Operator fencing contract").
package fence
