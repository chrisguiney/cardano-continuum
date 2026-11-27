package send

import "context"

// target lets internal/submit classify a Node's answer. The era is the
// Node's concern.
type target struct{ n Node }

func (t target) Submit(ctx context.Context, _ uint, tx []byte) error { return t.n.Submit(ctx, tx) }

func (target) Name() string { return "node" }
