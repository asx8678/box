package egress

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Event is one connection the box asked for, and the proxy's answer.
type Event struct {
	Host    string
	Port    uint16
	Allowed bool
	Reason  string // why it was blocked or failed; "" when it connected
}

// Server is the proxy. One listener serves SOCKS5 (socks5h://, which sends
// host names) and HTTP proxy requests (CONNECT, and plain http:// requests
// in absolute form): the first byte tells them apart.
type Server struct {
	Policy *Policy
	// Resolve and Dial default to the system's; tests replace them.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, addr netip.AddrPort) (net.Conn, error)
	Log     func(Event) // may be nil
}

var (
	errDenied  = errors.New("not in the allowed network")
	errPrivate = errors.New("resolves only to private addresses")
)

// Serve answers connections on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 5 {
		s.socks(c, br)
	} else {
		s.http(c, br)
	}
}

// open checks host:port against the policy, resolves the name here on the
// host, refuses private answers for names from box's own lists, and dials
// the address it checked, so a second DNS answer can't change it.
func (s *Server) open(host string, port uint16) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, ok := s.Policy.match(host, port)
	if !ok {
		return nil, s.log(host, port, errDenied)
	}
	var addrs []netip.Addr
	if r.ip.IsValid() {
		addrs = []netip.Addr{r.ip}
	} else {
		resolve := s.Resolve
		if resolve == nil {
			resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			}
		}
		all, err := resolve(ctx, host)
		if err != nil {
			return nil, s.log(host, port, fmt.Errorf("can't resolve: %w", err))
		}
		for _, a := range all {
			if r.trusted || Public(a) {
				addrs = append(addrs, a.Unmap())
			}
		}
		if len(addrs) == 0 {
			return nil, s.log(host, port, errPrivate)
		}
	}
	dial := s.Dial
	if dial == nil {
		dial = func(ctx context.Context, ap netip.AddrPort) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", ap.String())
		}
	}
	var err error
	for _, a := range addrs {
		var up net.Conn
		if up, err = dial(ctx, netip.AddrPortFrom(a, port)); err == nil {
			s.log(host, port, nil)
			return up, nil
		}
	}
	return nil, s.log(host, port, err)
}

// log records the outcome and returns err.
func (s *Server) log(host string, port uint16, err error) error {
	if s.Log != nil {
		e := Event{Host: host, Port: port, Allowed: err == nil}
		if err != nil {
			e.Reason = err.Error()
		}
		s.Log(e)
	}
	return err
}

// SOCKS5 (RFC 1928), CONNECT only, no authentication: the socket is the
// box's own.
func (s *Server) socks(c net.Conn, br *bufio.Reader) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if !contains(methods, 0) {
		c.Write([]byte{5, 0xff})
		return
	}
	c.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	reply := func(code byte) { c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	var host string
	switch req[3] {
	case 1, 4: // IPv4, IPv6
		b := make([]byte, map[byte]int{1: 4, 4: 16}[req[3]])
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		a, _ := netip.AddrFromSlice(b)
		host = a.Unmap().String()
	case 3: // a name
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	default:
		reply(8) // address type not supported
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	if req[1] != 1 {
		reply(7) // only CONNECT: no BIND, no UDP
		return
	}
	up, err := s.open(host, binary.BigEndian.Uint16(pb))
	switch {
	case errors.Is(err, errDenied), errors.Is(err, errPrivate):
		reply(2) // not allowed by ruleset
		return
	case err != nil:
		reply(4) // host unreachable
		return
	}
	defer up.Close()
	reply(0)
	splice(c, br, up)
}

// http serves CONNECT, and plain http:// requests, one per connection.
func (s *Server) http(c net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	target := req.Host
	if req.Method != http.MethodConnect {
		if req.URL.Host == "" {
			refuse(c, http.StatusBadRequest, "box's proxy only forwards requests to other hosts")
			return
		}
		target = req.URL.Host
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		host, portStr = target, "80"
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		refuse(c, http.StatusBadRequest, "bad port")
		return
	}
	up, err := s.open(host, uint16(port))
	switch {
	case errors.Is(err, errDenied), errors.Is(err, errPrivate):
		refuse(c, http.StatusForbidden, fmt.Sprintf("box: %s:%d is blocked: %v; allow it in the profile", host, port, err))
		return
	case err != nil:
		refuse(c, http.StatusBadGateway, fmt.Sprintf("box: %s:%d: %v", host, port, err))
		return
	}
	defer up.Close()
	if req.Method == http.MethodConnect {
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		splice(c, br, up)
		return
	}
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	req.Close = true // one request per connection: the next may go elsewhere
	if err := req.Write(up); err != nil {
		return
	}
	io.Copy(c, up)
}

func refuse(c net.Conn, code int, msg string) {
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s\n",
		code, http.StatusText(code), len(msg)+1, msg)
}

// splice copies both ways until both sides are done; in reads from c,
// including what was already buffered.
func splice(c net.Conn, in io.Reader, up net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst net.Conn, src io.Reader) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go half(up, in)
	go half(c, up)
	wg.Wait()
}

func contains(b []byte, x byte) bool {
	for _, v := range b {
		if v == x {
			return true
		}
	}
	return false
}
