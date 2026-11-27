// Package wire holds the value types the engine's inputs and decisions carry:
// log bodies, transaction and output identifiers, block digests and the
// chain-sync event. It has no behaviour beyond reading a block into a digest,
// so every engine component can share it without importing another.
package wire
