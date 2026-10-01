package egress

import "net"

// Inside the box, the bridge listens on these loopback ports, which the
// program's proxy variables name. Both forward to the same proxy socket,
// which tells the protocols apart.
const (
	SocksAddr = "127.0.0.1:1080"
	HTTPAddr  = "127.0.0.1:3128"
)

// Bridge forwards every connection to the loopback ports to the proxy
// socket. It returns once listening; the forwarding runs in the background
// for as long as the process lives.
func Bridge(socket string) error {
	for _, addr := range []string{SocksAddr, HTTPAddr} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					up, err := net.Dial("unix", socket)
					if err != nil {
						return
					}
					defer up.Close()
					splice(c, c, up)
				}()
			}
		}()
	}
	return nil
}
