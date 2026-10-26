package netutil

import (
	"net"
	"sync"
)

type LimitedListener struct {
	net.Listener
	mu       sync.Mutex
	total    int
	perIP    map[string]int
	maxTotal int
	maxPerIP int
	trusted  []*net.IPNet
}

func NewLimitedListener(listener net.Listener, maxTotal, maxPerIP int, trusted []*net.IPNet) *LimitedListener {
	return &LimitedListener{Listener: listener, perIP: make(map[string]int), maxTotal: maxTotal, maxPerIP: maxPerIP, trusted: trusted}
}

func (l *LimitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			conn.Close()
			continue
		}
		key := RateLimitKey(net.ParseIP(host))
		l.mu.Lock()
		limit := l.maxPerIP
		if IsTrustedProxy(net.ParseIP(host), l.trusted) {
			limit = l.maxTotal
		}
		if l.total >= l.maxTotal || l.perIP[key] >= limit {
			l.mu.Unlock()
			conn.Close()
			continue
		}
		l.total++
		l.perIP[key]++
		l.mu.Unlock()
		return &limitedConn{Conn: conn, release: func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			l.perIP[key]--
			if l.perIP[key] == 0 {
				delete(l.perIP, key)
			}
		}}, nil
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
	err     error
}

func (c *limitedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.release()
	})
	return c.err
}
