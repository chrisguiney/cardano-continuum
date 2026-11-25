package submit

import (
	"context"
	"fmt"
	"net"

	ouroboros "github.com/blinklabs-io/gouroboros"
)

// Dialer opens a connection to a node's node-to-client endpoint: a unix
// socket for a local node, or a forwarded TCP port.
type Dialer func(ctx context.Context) (net.Conn, error)

// Local submits over a node's local-tx-submission mini-protocol. Each
// Submit uses its own connection, so a hung node holds up only its caller.
type Local struct {
	dial  Dialer
	magic uint32
}

// NewLocal returns a target for the node dial reaches, on the network with
// the given magic.
func NewLocal(dial Dialer, magic uint32) *Local { return &Local{dial: dial, magic: magic} }

// Name implements Target.
func (*Local) Name() string { return "local-tx-submission" }

// Submit implements Target. A node's refusal is a *RejectedError; every
// other error is transport.
func (l *Local) Submit(ctx context.Context, era uint, tx []byte) error {
	conn, err := l.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn),
		ouroboros.WithNetworkMagic(l.magic),
		ouroboros.WithNodeToNode(false),
	)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("handshake: %w", err)
	}
	defer func() { _ = oc.Close() }()
	// SubmitTx waits on the node with no deadline of its own; closing the
	// connection is what unblocks it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	err = oc.LocalTxSubmission().Client.SubmitTx(uint16(era), tx)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if fs, ok := ledgerRejection(err); ok {
			return &RejectedError{Failures: fs}
		}
		return fmt.Errorf("submit: %w", err)
	}
	return nil
}
