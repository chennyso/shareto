package netutil_test

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shareto/internal/netutil"
)

func TestConcurrentPasswordAttemptReservations(t *testing.T) {
	limiter := netutil.NewRateLimiter(64)
	var accepted atomic.Int32
	var workers sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-gate
			finish, _ := limiter.BeginAttempt("share", net.ParseIP("2001:db8::1"))
			if finish != nil {
				accepted.Add(1)
				finish(false)
				finish(false)
			}
		}()
	}
	close(gate)
	workers.Wait()
	if accepted.Load() != netutil.MaxFailures {
		t.Fatalf("accepted %d password attempts", accepted.Load())
	}
	if finish, remaining := limiter.BeginAttempt("share", net.ParseIP("2001:db8::2")); finish != nil || remaining < 890 {
		t.Fatalf("IPv6 subnet lockout missing: remaining=%d", remaining)
	}
}

func TestRateLimiterRetainsPendingReservations(t *testing.T) {
	limiter := netutil.NewRateLimiter(1)
	finish, _ := limiter.BeginAttempt("one", net.ParseIP("192.0.2.1"))
	if finish == nil {
		t.Fatal("initial reservation rejected")
	}
	if other, _ := limiter.BeginAttempt("two", net.ParseIP("192.0.2.2")); other != nil {
		t.Fatal("pending reservation was evicted")
	}
	finish(true)
	other, _ := limiter.BeginAttempt("two", net.ParseIP("192.0.2.2"))
	if other == nil {
		t.Fatal("completed reservation could not be evicted")
	}
	other(true)
}

func TestConnectionAdmissionAndRelease(t *testing.T) {
	for _, limits := range []struct {
		name  string
		total int
		perIP int
	}{{"global", 1, 2}, {"client", 2, 1}} {
		t.Run(limits.name, func(t *testing.T) {
			raw, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener := netutil.NewLimitedListener(raw, limits.total, limits.perIP, nil)
			defer listener.Close()
			first, err := net.Dial("tcp", raw.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			accepted, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			incoming := make(chan net.Conn, 1)
			errorsCh := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					errorsCh <- err
					return
				}
				incoming <- conn
			}()
			second, err := net.Dial("tcp", raw.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := second.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("excess connection was not closed: %v", err)
			}
			accepted.Close()
			accepted.Close()
			third, err := net.Dial("tcp", raw.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer third.Close()
			select {
			case conn := <-incoming:
				conn.Close()
			case err := <-errorsCh:
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("connection capacity was not released")
			}
		})
	}
}
