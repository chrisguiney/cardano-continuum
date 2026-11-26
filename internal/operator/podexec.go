package operator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// execLimit bounds what one command may write: Config files are small.
const execLimit = 1 << 20

// PodExec runs commands in pods through the API server's exec subresource.
type PodExec struct {
	cfg  *rest.Config
	rest rest.Interface
}

// NewPodExec returns a PodExec that talks to the API server cfg names.
func NewPodExec(cfg *rest.Config) (*PodExec, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("pod exec client: %w", err)
	}
	return &PodExec{cfg: cfg, rest: cs.CoreV1().RESTClient()}, nil
}

// Exec runs argv in container of pod and returns its standard output. It
// tries WebSocket first and falls back to SPDY, as kubectl does.
func (e *PodExec) Exec(ctx context.Context, pod types.NamespacedName, container string, argv []string) ([]byte, error) {
	u := e.rest.Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: argv, Stdout: true, Stderr: true}, scheme.ParameterCodec).
		URL()
	ws, err := remotecommand.NewWebSocketExecutor(e.cfg, http.MethodGet, u.String())
	if err != nil {
		return nil, fmt.Errorf("exec %s in %s: %w", argv[0], pod, err)
	}
	spdy, err := remotecommand.NewSPDYExecutor(e.cfg, http.MethodPost, u)
	if err != nil {
		return nil, fmt.Errorf("exec %s in %s: %w", argv[0], pod, err)
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return nil, fmt.Errorf("exec %s in %s: %w", argv[0], pod, err)
	}
	stdout, stderr := &capped{}, &capped{}
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr}); err != nil {
		return nil, fmt.Errorf("exec %s in %s/%s: %w: %s", argv[0], pod, container, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

var errExecOutput = errors.New("exec output over 1 MiB")

// capped is a buffer that refuses to grow past execLimit.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if c.Len()+len(p) > execLimit {
		return 0, errExecOutput
	}
	return c.Buffer.Write(p)
}
