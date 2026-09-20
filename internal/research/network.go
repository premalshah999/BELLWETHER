package research

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Search results are untrusted input. Validate again at dial time so a public
// hostname cannot redirect or rebind to the database, metadata service or LAN.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2002::/16"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func articleURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("article: invalid public HTTP URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return nil, fmt.Errorf("article: private host refused")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return nil, fmt.Errorf("article: private address refused")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return nil, fmt.Errorf("article: non-web port refused")
	}
	u.Fragment = ""
	return u, nil
}

func publicTransport() *http.Transport {
	return &http.Transport{
		// Deliberately no environment proxy: it would resolve untrusted hosts
		// outside the address checks below.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("article: no public address")
			}
			for _, ip := range ips {
				if !publicIP(ip) {
					return nil, fmt.Errorf("article: DNS returned a private address")
				}
			}
			d := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
			var last error
			for _, ip := range ips {
				conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
		TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 12 * time.Second,
		MaxConnsPerHost: 2, MaxIdleConns: 32, IdleConnTimeout: time.Minute,
		ForceAttemptHTTP2: true,
	}
}
