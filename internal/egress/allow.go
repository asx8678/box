// Package egress is box's network proxy for profiles with network =
// "restricted". The sandbox has no network of its own; its only way out is
// a Unix socket to the proxy, which runs in box on the host and lets
// through only the hosts the profile allows. Inside the box, a bridge in
// box's init turns that socket back into the loopback ports that programs'
// proxy settings point at.
package egress

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Entry is one allowed host as a profile writes it: "host", "*.suffix" or an
// IP address, each optionally with ":port" ("[2001:db8::1]:443" for IPv6).
// Trusted entries are the ones the user typed: their names may resolve to
// private addresses, such as a company wiki on the LAN. Box's own lists may
// not, so a public name can't be pointed at the LAN.
type Entry struct {
	Host    string
	Trusted bool
}

type rule struct {
	name     string // lower case; the suffix after "*." for a wildcard
	wildcard bool
	ip       netip.Addr // set for an address entry
	port     uint16     // 0 means 80 and 443
	trusted  bool
}

// Policy decides which hosts and ports the box may reach.
type Policy struct {
	rules []rule
}

// NewPolicy parses the allowed entries. They were checked when the profile
// was loaded, so an error here means a bug, not a typo.
func NewPolicy(entries []Entry) (*Policy, error) {
	p := &Policy{}
	for _, e := range entries {
		r, err := parse(e.Host)
		if err != nil {
			return nil, err
		}
		r.trusted = e.Trusted || r.ip.IsValid() // an address is always typed by the user
		p.rules = append(p.rules, r)
	}
	return p, nil
}

func parse(s string) (rule, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return rule{ip: ap.Addr().Unmap(), port: ap.Port()}, nil
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return rule{ip: a.Unmap()}, nil
	}
	name, port, hasPort := strings.Cut(strings.ToLower(s), ":")
	r := rule{name: name}
	if hasPort {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return rule{}, fmt.Errorf("allowed host %q: bad port", s)
		}
		r.port = uint16(n)
	}
	if suffix, ok := strings.CutPrefix(name, "*."); ok {
		r.name, r.wildcard = suffix, true
	}
	if r.name == "" || strings.Contains(r.name, "*") {
		return rule{}, fmt.Errorf("allowed host %q is not a host", s)
	}
	return r, nil
}

// match returns the rule that allows host:port. A name matches exact
// entries and "*.suffix" entries with at least one more label in front;
// an address matches only an address entry, so a program can't reach a
// host by its IP when only its name is allowed.
func (p *Policy) match(host string, port uint16) (rule, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	for _, r := range p.rules {
		if r.port == 0 && port != 80 && port != 443 || r.port != 0 && r.port != port {
			continue
		}
		switch {
		case err == nil:
			if r.ip.IsValid() && r.ip == ip.Unmap() {
				return r, true
			}
		case r.wildcard:
			if strings.HasSuffix(host, "."+r.name) {
				return r, true
			}
		case !r.ip.IsValid() && r.name == host:
			return r, true
		}
	}
	return rule{}, false
}

// Public reports whether a is an address on the internet, not on this
// machine, the LAN, a carrier's shared space or the WSL host's network.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a) && !thisNetwork.Contains(a)
}

var (
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
)
