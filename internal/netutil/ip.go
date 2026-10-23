package netutil

import (
	"net"
	"net/http"
	"strings"
)

// ParseCIDRs parses a list of CIDR strings into []*net.IPNet.
func ParseCIDRs(cidrStrs []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(cidrStrs))
	for _, s := range cidrStrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			// If missing mask, append host mask
			if ip := net.ParseIP(s); ip != nil {
				if ip.To4() != nil {
					s += "/32"
				} else {
					s += "/128"
				}
			}
		}
		_, ipNet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// IsTrustedProxy checks whether an IP is within the trusted CIDR ranges.
func IsTrustedProxy(ip net.IP, trustedCIDRs []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, network := range trustedCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ExtractClientIP retrieves the real client IP according to the trusted proxies policy.
// If remote IP is not trusted, X-Forwarded-For is strictly ignored.
// If remote IP is trusted, X-Forwarded-For is traversed from right to left,
// returning the first untrusted IP. If all are trusted, returns the leftmost IP.
func ExtractClientIP(r *http.Request, trustedCIDRs []*net.IPNet) net.IP {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.TrimSpace(remoteHost))
	if remoteIP == nil {
		return net.IPv4(127, 0, 0, 1)
	}

	if !IsTrustedProxy(remoteIP, trustedCIDRs) {
		return remoteIP
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remoteIP
	}

	parts := strings.Split(xff, ",")
	validIPs := make([]net.IP, 0, len(parts))
	for _, p := range parts {
		ip := net.ParseIP(strings.TrimSpace(p))
		if ip != nil {
			validIPs = append(validIPs, ip)
		}
	}

	if len(validIPs) == 0 {
		return remoteIP
	}

	// Traverse from right to left
	for i := len(validIPs) - 1; i >= 0; i-- {
		if !IsTrustedProxy(validIPs[i], trustedCIDRs) {
			return validIPs[i]
		}
	}

	// If all IPs in X-Forwarded-For are trusted proxies, return the leftmost IP
	return validIPs[0]
}

// RateLimitKey returns /32 for IPv4, or /64 for IPv6.
func RateLimitKey(ip net.IP) string {
	if ip == nil {
		return "unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	v6 := ip.To16()
	if v6 == nil {
		return ip.String()
	}
	masked := v6.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}

// SessionSubnetKey returns /24 for IPv4, or /64 for IPv6.
func SessionSubnetKey(ip net.IP) string {
	if ip == nil {
		return "unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		masked := v4.Mask(net.CIDRMask(24, 32))
		return masked.String() + "/24"
	}
	v6 := ip.To16()
	if v6 == nil {
		return ip.String()
	}
	masked := v6.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}
