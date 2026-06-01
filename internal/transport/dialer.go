// Package transport provides low-level TCP dialing with HTTP proxy CONNECT
// support and the payload injection engine.
package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/goodyrussia/SSHCustom-Magisk/internal/dnsx"
)

// Dialer is a TCP dialer with optional HTTP proxy CONNECT support.
type Dialer struct {
	ProxyHost string
	ProxyPort int
	Timeout   time.Duration
}

// DialContext dials the target address, optionally tunneling through an HTTP
// CONNECT proxy. Host resolution for the target uses dnsx.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.ProxyHost != "" && d.ProxyPort > 0 {
		return d.dialProxy(ctx, network, addr)
	}
	return dnsx.New().DialContext(ctx, network, addr)
}

// dialProxy performs an HTTP CONNECT through the configured proxy, then returns
// the raw tunneled connection.
func (d *Dialer) dialProxy(ctx context.Context, network, targetAddr string) (net.Conn, error) {
	proxyAddr := fmt.Sprintf("%s:%d", d.ProxyHost, d.ProxyPort)
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 25 * time.Second
	}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dnsx.New().DialContext(dialCtx, network, proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy dial %s: %w", proxyAddr, err)
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n",
		targetAddr, targetAddr)

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT write: %w", err)
	}

	// Read proxy response — expect "HTTP/1.x 200"
	resp := make([]byte, 4096)
	n, err := conn.Read(resp)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT read: %w", err)
	}
	status := string(resp[:n])
	if len(status) < 12 || status[:7] != "HTTP/1." {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT bad response: %s", status)
	}
	// Accept 2xx
	if status[9] != '2' {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT rejected: %s", status[:16])
	}

	// Drain the remaining HTTP headers until \r\n\r\n
	buf := make([]byte, 1)
	crlfCount := 0
	for crlfCount < 4 && n > 0 {
		n, err = conn.Read(buf)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT drain: %w", err)
		}
		if buf[0] == '\r' || buf[0] == '\n' {
			crlfCount++
		} else {
			crlfCount = 0
		}
	}

	return conn, nil
}

// resolveHost resolves a hostname to an IPv4 address using dnsx.
func resolveHost(ctx context.Context, host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	ips, err := dnsx.New().Lookup(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	return ips[0].String(), nil
}
