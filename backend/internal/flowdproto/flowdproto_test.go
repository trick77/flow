package flowdproto

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompatible(t *testing.T) {
	for _, c := range []struct {
		min, max int
		want     bool
	}{{1, 1, true}, {2, 3, false}, {0, 0, false}, {1, 5, true}} {
		if got := Compatible(Hello{MinProtocol: c.min, Protocol: c.max}); got != c.want {
			t.Errorf("Compatible(%d..%d) = %v", c.min, c.max, got)
		}
	}
}

// The route constants are net/http patterns: a mux takes them all without a conflict.
func TestRoutesAreServeMuxPatterns(t *testing.T) {
	mux := http.NewServeMux()
	for _, r := range []string{RouteHello, RouteEnv, RouteRepos, RouteSessionPut, RouteSessionGet,
		RouteSessionDelete, RouteCommands, RouteEvents, RouteWorktrees, RouteDiff, RouteControl, RouteControlReply,
		RouteTerminalPut, RouteTerminalAttach, RouteTerminalDelete, RoutePort, RouteJobPut, RouteJobGet, RouteJobLog} {
		if !strings.Contains(r, "/v1/") {
			t.Errorf("%q is outside /v1", r)
		}
		mux.HandleFunc(r, func(http.ResponseWriter, *http.Request) {})
	}
}
