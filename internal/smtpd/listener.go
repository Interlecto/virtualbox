package smtpd

import (
	"io"
	"net"
	"sync"
	"time"
)

// limitListener caps the number of simultaneous connections, in total and per
// remote IP. Connections over the limit are answered with a polite 421 and
// closed immediately, so a flood cannot exhaust file descriptors or goroutines.
//
// The limit is applied at accept time (not at EHLO) so idle connections that
// never speak are counted too.
type limitListener struct {
	net.Listener
	maxTotal int
	maxPerIP int

	mu    sync.Mutex
	total int
	perIP map[string]int
}

func newLimitListener(l net.Listener, maxTotal, maxPerIP int) *limitListener {
	return &limitListener{
		Listener: l,
		maxTotal: maxTotal,
		maxPerIP: maxPerIP,
		perIP:    make(map[string]int),
	}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c.RemoteAddr())
		if !l.acquire(ip) {
			go rejectBusy(c)
			continue
		}
		return &limitConn{Conn: c, l: l, ip: ip}, nil
	}
}

func (l *limitListener) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxTotal > 0 && l.total >= l.maxTotal {
		return false
	}
	if l.maxPerIP > 0 && l.perIP[ip] >= l.maxPerIP {
		return false
	}
	l.total++
	l.perIP[ip]++
	return true
}

func (l *limitListener) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perIP[ip] <= 1 {
		delete(l.perIP, ip)
	} else {
		l.perIP[ip]--
	}
}

// active returns the current number of accepted connections.
func (l *limitListener) active() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

func rejectBusy(c net.Conn) {
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(c, "421 4.3.2 Too many connections, try again later\r\n")
	_ = c.Close()
}

type limitConn struct {
	net.Conn
	l    *limitListener
	ip   string
	once sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.l.release(c.ip) })
	return err
}

// remoteIP returns the host part of addr, or its string form if it has none.
func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
