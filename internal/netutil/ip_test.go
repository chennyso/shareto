package netutil_test

import (
	"net"
	"net/http"
	"testing"

	"shareto/internal/netutil"
)

func TestExtractClientIP(t *testing.T) {
	trusted, err := netutil.ParseCIDRs([]string{"127.0.0.1/32", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRs failed: %v", err)
	}

	// Case 1: Direct untrusted IP
	req, _ := http.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.1:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	clientIP := netutil.ExtractClientIP(req, trusted)
	if clientIP.String() != "198.51.100.1" {
		t.Fatalf("Expected direct untrusted IP 198.51.100.1, got %s", clientIP.String())
	}

	// Case 2: Trusted proxy with X-Forwarded-For
	req2, _ := http.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("X-Forwarded-For", "203.0.113.195, 10.0.0.5")
	clientIP2 := netutil.ExtractClientIP(req2, trusted)
	if clientIP2.String() != "203.0.113.195" {
		t.Fatalf("Expected rightmost untrusted IP 203.0.113.195, got %s", clientIP2.String())
	}
}

func TestSubnetKeys(t *testing.T) {
	v4 := net.ParseIP("192.168.1.50")
	if netutil.RateLimitKey(v4) != "192.168.1.50" {
		t.Fatalf("Unexpected IPv4 rate limit key: %s", netutil.RateLimitKey(v4))
	}
	if netutil.SessionSubnetKey(v4) != "192.168.1.0/24" {
		t.Fatalf("Unexpected IPv4 session subnet key: %s", netutil.SessionSubnetKey(v4))
	}

	v6 := net.ParseIP("2001:db8:abcd:0012::dead:beef")
	if netutil.RateLimitKey(v6) != "2001:db8:abcd:12::/64" {
		t.Fatalf("Unexpected IPv6 rate limit key: %s", netutil.RateLimitKey(v6))
	}
	if netutil.SessionSubnetKey(v6) != "2001:db8:abcd:12::/64" {
		t.Fatalf("Unexpected IPv6 session subnet key: %s", netutil.SessionSubnetKey(v6))
	}
}
