package devbuild

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// OverlayDockerfile layers the binary named by its base name over the one in
// the upstream image pinned by baseDigest. Everything else, the entrypoint
// and user included, is inherited, so the runtime is identical to main's.
func OverlayDockerfile(baseDigest, binPath string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "FROM %s@%s\n", ident.UpstreamDingoImage, baseDigest)
	fmt.Fprintf(&b, "COPY --chmod=0755 %s %s\n", path.Base(binPath), binPath)
	return b.Bytes()
}

// BinaryPath finds where dingo's Dockerfile installs bin in its runtime
// image: the destination of the `COPY --from=build /code/<bin> <dest>` line.
func BinaryPath(dockerfile []byte, bin string) (string, error) {
	for _, line := range strings.Split(string(dockerfile), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.EqualFold(f[0], "COPY") {
			continue
		}
		args := f[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:]
		}
		if len(args) != 2 || path.Base(args[0]) != bin || !strings.HasPrefix(args[0], "/code/") {
			continue
		}
		dest := args[1]
		if strings.HasSuffix(dest, "/") {
			dest = path.Join(dest, bin)
		}
		return dest, nil
	}
	return "", fmt.Errorf("dockerfile has no COPY of /code/%s into the runtime image", bin)
}

// ldflags are dingo's Makefile ldflags (GO_LDFLAGS): strip, and set the
// version package's Version and CommitHash.
func ldflags(module, version, commit string) string {
	pkg := module + "/internal/version"
	return fmt.Sprintf("-s -w -X %s.Version=%s -X %s.CommitHash=%s", pkg, version, pkg, commit)
}
