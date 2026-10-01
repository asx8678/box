package egress

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func TestMatch(t *testing.T) {
	p, err := NewPolicy([]Entry{
		{Host: "example.com"}, {Host: "*.example.org"}, {Host: "git.example.net:8443"},
		{Host: "10.0.0.5"}, {Host: "[2001:db8::1]:22"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		host string
		port uint16
		want bool
	}{
		{"example.com", 443, true},
		{"Example.COM.", 80, true},      // case and a trailing dot
		{"example.com", 8080, false},    // no port: 80 and 443 only
		{"www.example.com", 443, false}, // exact means exact
		{"a.example.org", 443, true},
		{"a.b.example.org", 443, true},
		{"example.org", 443, false}, // *. needs a label in front
		{"badexample.org", 443, false},
		{"git.example.net", 8443, true},
		{"git.example.net", 443, false},
		{"10.0.0.5", 443, true},
		{"2001:db8::1", 22, true},
		{"[2001:db8::1]", 22, true},
		{"93.184.216.34", 443, false}, // an address only when listed
		{"other.com", 443, false},
	} {
		if _, got := p.match(tt.host, tt.port); got != tt.want {
			t.Errorf("%s:%d: %v, want %v", tt.host, tt.port, got, tt.want)
		}
	}
}

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1": true,
		"10.1.2.3": false, "192.168.1.1": false, "172.20.0.1": false, "127.0.0.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false,
		"fd00::1": false, "fe80::1": false, "::ffff:192.168.1.1": false,
	} {
		if got := Public(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v, want %v", s, got, want)
		}
	}
}

// harness runs a proxy whose "internet" is one local server that answers
// every connection with the address it was asked to dial.
type harness struct {
	addr   string
	events []Event
	mu     sync.Mutex
}

func newHarness(t *testing.T, entries ...Entry) *harness {
	t.Helper()
	p, err := NewPolicy(entries)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{}
	s := &Server{
		Policy: p,
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			switch host {
			case "lan.example.com", "wiki.corp":
				return []netip.Addr{netip.MustParseAddr("192.168.1.5")}, nil
			case "nowhere.example.com":
				return nil, errors.New("no such host")
			}
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		Dial: func(_ context.Context, ap netip.AddrPort) (net.Conn, error) {
			a, b := net.Pipe()
			go func() {
				defer b.Close()
				br := bufio.NewReader(b)
				if ap.Port() == 80 { // a plain HTTP server
					if req, err := http.ReadRequest(br); err == nil {
						io.WriteString(b, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"+ap.String()+" "+req.URL.String())
					}
					return
				}
				line, _ := br.ReadString('\n')
				io.WriteString(b, ap.String()+" got "+line)
			}()
			return a, nil
		},
		Log: func(e Event) { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() },
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l)
	h.addr = l.Addr().String()
	return h
}

// socks connects through SOCKS5 by name and returns the reply code and what
// the far end said to "hi".
func (h *harness) socks(t *testing.T, host string, port uint16) (byte, string) {
	t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte{5, 1, 0})
	buf := make([]byte, 10)
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[1] != 0 {
		t.Fatalf("greeting: %v %v", buf[:2], err)
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, port)
	c.Write(req)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if buf[1] != 0 {
		return buf[1], ""
	}
	io.WriteString(c, "hi\n")
	out, _ := io.ReadAll(c)
	return 0, string(out)
}

func (h *harness) connect(t *testing.T, target string) string {
	t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\nhi\n")
	out, _ := io.ReadAll(c)
	return string(out)
}

func TestSocks(t *testing.T) {
	h := newHarness(t, Entry{Host: "example.com"}, Entry{Host: "lan.example.com"}, Entry{Host: "wiki.corp", Trusted: true})
	if code, out := h.socks(t, "example.com", 443); code != 0 || out != "93.184.216.34:443 got hi\n" {
		t.Errorf("allowed: code %d, %q", code, out)
	}
	if code, _ := h.socks(t, "other.com", 443); code != 2 {
		t.Errorf("not allowed: code %d, want 2", code)
	}
	// A listed name from box's lists may not lead into the LAN...
	if code, _ := h.socks(t, "lan.example.com", 443); code != 2 {
		t.Errorf("private answer: code %d, want 2", code)
	}
	// ...but one the user typed may.
	if code, out := h.socks(t, "wiki.corp", 443); code != 0 || !strings.HasPrefix(out, "192.168.1.5:443") {
		t.Errorf("trusted private: code %d, %q", code, out)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) != 4 || !h.events[0].Allowed || h.events[1].Allowed || h.events[1].Host != "other.com" {
		t.Errorf("events %+v", h.events)
	}
}

func TestHTTPConnect(t *testing.T) {
	h := newHarness(t, Entry{Host: "*.example.com"})
	if out := h.connect(t, "api.example.com:443"); !strings.Contains(out, "200 Connection established") ||
		!strings.HasSuffix(out, "93.184.216.34:443 got hi\n") {
		t.Errorf("allowed: %q", out)
	}
	if out := h.connect(t, "example.com:443"); !strings.Contains(out, "403") || !strings.Contains(out, "example.com:443 is blocked") {
		t.Errorf("blocked: %q", out)
	}
	if out := h.connect(t, "nowhere.example.com:443"); !strings.Contains(out, "502") {
		t.Errorf("unresolvable: %q", out)
	}
}

func TestPlainHTTP(t *testing.T) {
	h := newHarness(t, Entry{Host: "example.com"})
	get := func(url string) string {
		c, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		io.WriteString(c, "GET "+url+" HTTP/1.1\r\nHost: x\r\nProxy-Connection: keep-alive\r\n\r\n")
		out, _ := io.ReadAll(c)
		return string(out)
	}
	// The far end gets the request in origin form.
	if out := get("http://example.com/a?b=1"); !strings.HasSuffix(out, "93.184.216.34:80 /a?b=1") {
		t.Errorf("allowed: %q", out)
	}
	if out := get("http://other.com/"); !strings.Contains(out, "403") {
		t.Errorf("blocked: %q", out)
	}
}
