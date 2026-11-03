package compose

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// portBindings reads one service's `ports:` entries into the host side of each
// publish: the host address it binds (empty for every interface) and the host
// port.
//
// It accepts exactly the two forms the catalog uses, "HOST:CONTAINER" and
// "IP:HOST:CONTAINER" with an IPv4 address, and refuses everything else: a
// bare container port (Docker picks the host port), a /protocol suffix, a
// bracketed IPv6 host, a range, the long mapping syntax, a non-string scalar
// and a ${VAR}. An entry this cannot read is a binding the publisher rule
// cannot judge, so it fails closed rather than being skipped
// (geekdojo/geekdojo-brain#551).
func portBindings(service string, entries []any) ([]tileschema.PortBinding, error) {
	out := make([]tileschema.PortBinding, 0, len(entries))
	for _, e := range entries {
		b, ok := parsePortEntry(e)
		if !ok {
			return nil, fmt.Errorf("service %q: ports entry %q is not \"HOST:CONTAINER\" or \"IP:HOST:CONTAINER\"", service, fmt.Sprint(e))
		}
		out = append(out, b)
	}
	return out, nil
}

// parsePortEntry reads one short-syntax entry, reporting false for any form
// portBindings does not accept.
func parsePortEntry(e any) (tileschema.PortBinding, bool) {
	s, ok := e.(string)
	if !ok {
		return tileschema.PortBinding{}, false
	}
	parts := strings.Split(s, ":")
	var hostIP string
	switch len(parts) {
	case 2:
	case 3:
		addr, err := netip.ParseAddr(parts[0])
		if err != nil || !addr.Is4() {
			return tileschema.PortBinding{}, false
		}
		hostIP = parts[0]
		parts = parts[1:]
	default:
		return tileschema.PortBinding{}, false
	}
	published, ok := portNumber(parts[0])
	if !ok {
		return tileschema.PortBinding{}, false
	}
	if _, ok := portNumber(parts[1]); !ok {
		return tileschema.PortBinding{}, false
	}
	return tileschema.PortBinding{HostIP: hostIP, Published: published}, true
}

// portNumber parses a decimal port in 1-65535, and nothing else: no sign, no
// range, no suffix.
func portNumber(s string) (int, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}
