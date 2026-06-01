// Package dnsx provides an Android-aware DNS resolver for SSH host resolution.
// It tries /system/bin/ping first (carrier DNS), falls back to Go net.LookupHost.
// Results are cached for 5 minutes.
package dnsx

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	cacheTTL     = 5 * time.Minute
	pingTimeout  = 3 * time.Second
	tcpKeepAlive = 15 * time.Second
)

var (
	shared     *Resolver
	sharedOnce sync.Once
)

// Resolver performs Android-aware DNS resolution with caching.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	ips     []net.IP
	expires time.Time
}

// New returns the shared process-wide resolver.
func New() *Resolver {
	sharedOnce.Do(func() {
		shared = &Resolver{cache: make(map[string]cacheEntry)}
	})
	return shared
}

// DialContext resolves host (if not already an IP) using carrier-friendly DNS
// and dials the first reachable address. The returned connection has TCP
// keepalive enabled.
func (r *Resolver) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dnsx: split %q: %w", addr, err)
	}

	if net.ParseIP(host) != nil {
		d := net.Dialer{KeepAlive: tcpKeepAlive}
		return d.DialContext(ctx, network, addr)
	}

	ips, err := r.Lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("dnsx: resolve %q: %w", host, err)
	}

	d := net.Dialer{KeepAlive: tcpKeepAlive}
	var lastErr error
	for _, ip := range ips {
		target := net.JoinHostPort(ip.String(), port)
		conn, derr := d.DialContext(ctx, network, target)
		if derr == nil {
			log.Printf("[dnsx] %s -> %s", host, ip)
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no reachable addresses for %s", host)
	}
	return nil, lastErr
}

// Lookup resolves host to IPv4 addresses using /system/bin/ping first, then
// Go's net.LookupHost as fallback. Results are cached for 5 minutes.
func (r *Resolver) Lookup(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSuffix(host, ".")

	r.mu.Lock()
	if e, ok := r.cache[host]; ok && time.Now().Before(e.expires) {
		ips := e.ips
		r.mu.Unlock()
		return ips, nil
	}
	r.mu.Unlock()

	ips, err := resolveHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dnsx: no addresses for %s", host)
	}

	r.mu.Lock()
	r.cache[host] = cacheEntry{ips: ips, expires: time.Now().Add(cacheTTL)}
	r.mu.Unlock()
	return ips, nil
}

// resolveHost tries /system/bin/ping first (carrier DNS on Android), then
// Go's net.LookupHost as a fallback.
func resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	// Try carrier DNS via ping. On Android, /system/bin/ping uses the carrier's
	// DNS which can resolve carrier-specific bug hosts that public DNS cannot.
	if ips, err := resolveViaPing(ctx, host); err == nil && len(ips) > 0 {
		return ips, nil
	}

	// Fall back to Go's net resolver
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dnsx: no IPv4 addresses for %s", host)
	}
	return ips, nil
}

// resolveViaPing runs /system/bin/ping -c1 -W<sec> <host> and extracts the IP
// from the output. This uses the carrier's DNS resolver on Android.
func resolveViaPing(ctx context.Context, host string) ([]net.IP, error) {
	dl, ok := ctx.Deadline()
	timeout := pingTimeout
	if ok {
		remaining := time.Until(dl)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	sec := int(timeout.Seconds())
	if sec < 1 {
		sec = 1
	}

	cmd := exec.CommandContext(ctx, "/system/bin/ping", "-c", "1", "-W", fmt.Sprintf("%d", sec), host)
	out, err := cmd.Output()
	if err != nil {
		// ping may not exist; try toybox ping
		cmd2 := exec.CommandContext(ctx, "/system/bin/toybox", "ping", "-c", "1", "-W", fmt.Sprintf("%d", sec), host)
		out, err = cmd2.Output()
		if err != nil {
			return nil, err
		}
	}

	// Parse "PING host (1.2.3.4) 56(84) bytes of data." or similar
	line := string(out)
	// Look for the IP in parentheses
	start := strings.Index(line, "(")
	if start < 0 {
		return nil, fmt.Errorf("dnsx: ping output unexpected: %s", strings.TrimSpace(line))
	}
	end := strings.Index(line[start:], ")")
	if end < 0 {
		return nil, fmt.Errorf("dnsx: ping output unexpected: %s", strings.TrimSpace(line))
	}
	ipStr := line[start+1 : start+end]
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("dnsx: ping returned non-IP: %q", ipStr)
	}
	return []net.IP{ip.To4()}, nil
}
