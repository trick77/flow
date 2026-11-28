package sandboxes

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// OffSandboxNetwork refuses every request that reaches Flow over the sandbox network: its local or its remote
// address lies in subnet. Flow always dials flowd and never needs to be dialled from there, but Flow's container
// sits on that Docker network to do so, and sandboxes reach it at L2 (the host cannot filter bridged traffic
// without br_netfilter): a listener on every interface would serve the browser API to them.
//
// Flow's container must therefore be on the reverse proxy's network too, and the proxy must reach it there
// (Traefik: label traefik.docker.network naming that network); a container whose only network is the sandbox
// network refuses every request but loopback ones. That is logged as an error at start.
//
// IPv6 link-local addresses (remote or local) are refused too: the sandbox network has no IPv6 subnet to judge
// by, and a sandbox that got IPv6 frames through to Flow's leg on it would speak link-local there (the proxy never
// does). Judged by address only, not by the interface a request came in on: two global IPv6 addresses (a leg
// that took a SLAAC address from a sandbox's router advertisement) pass. What keeps IPv6 off that leg is
// host-setup's drop of all IPv6 on the bridge and Docker leaving the leg without IPv6.
func OffSandboxNetwork(h http.Handler, subnet netip.Prefix) http.Handler {
	return offSandboxNetwork(h, subnet, net.InterfaceAddrs, slog.Default())
}

func offSandboxNetwork(h http.Handler, subnet netip.Prefix, addrs func() ([]net.Addr, error), log *slog.Logger) http.Handler {
	if list, err := addrs(); err == nil && subnet.IsValid() {
		other := false
		for _, a := range list {
			p, err := netip.ParsePrefix(a.String())
			if err != nil || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			if !subnet.Contains(p.Addr().Unmap()) {
				other = true
			}
		}
		if !other {
			log.Error("every address of this host outside loopback is on the sandbox network: the browser API refuses "+
				"every request that does not come over loopback; attach Flow to the reverse proxy's network and let "+
				"the proxy reach it there (traefik.docker.network)", "subnet", subnet)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if onSandboxNetwork(subnet, r.RemoteAddr) || local != nil && onSandboxNetwork(subnet, local.String()) {
			w.Header().Set("Connection", "close")
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeForbidden, "not served on the sandbox network"))
			return
		}
		h.ServeHTTP(w, r)
	})
}

// onSandboxNetwork: hostport lies in subnet or is IPv6 link-local (with or without a zone).
func onSandboxNetwork(subnet netip.Prefix, hostport string) bool {
	ap, err := netip.ParseAddrPort(hostport)
	a := ap.Addr().Unmap()
	return err == nil && (a.Is6() && a.IsLinkLocalUnicast() || subnet.IsValid() && subnet.Contains(a))
}
