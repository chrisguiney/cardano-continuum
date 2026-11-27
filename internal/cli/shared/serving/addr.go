package serving

import (
	"fmt"
	"net"
	"net/netip"
)

// CheckAddr refuses a listen address that is not loopback unless the
// operator asked for it: the API has no authentication.
func CheckAddr(addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr: %w", err)
	}
	if allowRemote || host == "localhost" {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %s is not loopback; the API has no authentication (use --allow-remote to override)", addr)
	}
	return nil
}
