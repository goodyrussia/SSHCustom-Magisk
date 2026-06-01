// Package ssh provides the SSH client with all transport modes and payload
// injection support.
package ssh

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/goodyrussia/SSHCustom-Magisk/internal/dnsx"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/transport"
)

// TransportMode configures how the TCP connection to the SSH server is established.
type TransportMode string

const (
	ModeDirect       TransportMode = "direct"
	ModeSNI          TransportMode = "sni"
	ModeSNIHTTPProxy TransportMode = "sni_http_proxy"
)

// ConnectConfig holds everything needed to dial the SSH server.
type ConnectConfig struct {
	Host              string
	Port              int
	User              string
	Password          string
	Mode              TransportMode
	SNIHost           string
	HTTPProxyHost     string
	HTTPProxyPort     int
	PayloadEnabled    bool
	Payload           string
	PayloadOpts       transport.PayloadOpts
	ConnectTimeout    time.Duration
	KeepAliveInterval time.Duration
	KeepAliveMax      int
}

// Client wraps an active SSH client connection.
type Client struct {
	sshConn *xssh.Client
	cfg     ConnectConfig
	ctx     context.Context
	cancel  context.CancelFunc

	activeConns int32
}

// ActiveConns returns the current number of in-flight proxied connections.
func (c *Client) ActiveConns() int { return int(atomic.LoadInt32(&c.activeConns)) }

// AddConn increments the active connection counter.
func (c *Client) AddConn() { atomic.AddInt32(&c.activeConns, 1) }

// RemoveConn decrements the active connection counter.
func (c *Client) RemoveConn() { atomic.AddInt32(&c.activeConns, -1) }

// Dial establishes an SSH connection using the configured transport mode.
func Dial(ctx context.Context, cfg ConnectConfig) (*Client, error) {
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 25 * time.Second
	}

	tcpConn, err := dialTransport(ctx, cfg, timeout)
	if err != nil {
		return nil, fmt.Errorf("transport dial: %w", err)
	}

	keepAliveInterval := cfg.KeepAliveInterval
	if keepAliveInterval == 0 {
		keepAliveInterval = 30 * time.Second
	}
	keepAliveMax := cfg.KeepAliveMax
	if keepAliveMax == 0 {
		keepAliveMax = 3
	}

	sshCfg := &xssh.ClientConfig{
		User:            cfg.User,
		Auth:            []xssh.AuthMethod{xssh.Password(cfg.Password)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         timeout,
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	sshConn, chans, reqs, err := xssh.NewClientConn(tcpConn, addr, sshCfg)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	cliCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		sshConn: xssh.NewClient(sshConn, chans, reqs),
		cfg:     cfg,
		ctx:     cliCtx,
		cancel:  cancel,
	}

	go c.keepAlive(keepAliveInterval, keepAliveMax)
	return c, nil
}

// keepAlive sends SSH keepalive requests and closes the connection if the
// server stops responding.
func (c *Client) keepAlive(interval time.Duration, maxMissed int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			_, _, err := c.sshConn.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				missed++
				if missed >= maxMissed {
					c.sshConn.Close()
					return
				}
			} else {
				missed = 0
			}
		}
	}
}

// dialTransport creates the raw TCP (or TLS-wrapped) connection to the server.
func dialTransport(ctx context.Context, cfg ConnectConfig, timeout time.Duration) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	switch cfg.Mode {
	case ModeDirect:
		conn, err := dnsx.New().DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}

		// Optional payload injection before SSH handshake
		if cfg.PayloadEnabled && cfg.Payload != "" {
			target := transport.Target{Host: cfg.Host, Port: cfg.Port}
			opts := cfg.PayloadOpts
			if err := transport.WritePayload(conn, cfg.Payload, target, opts); err != nil {
				conn.Close()
				return nil, fmt.Errorf("payload write: %w", err)
			}
			log.Printf("[payload] sent to %s (mode=%s)", addr, opts.InjectionType)

			// Wait for SSH banner, discarding any HTTP responses
			out, derr := awaitSSHBanner(ctx, conn)
			if derr != nil {
				conn.Close()
				return nil, derr
			}
			return out, nil
		}
		return conn, nil

	case ModeSNI:
		raw, err := dnsx.New().DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		sni := cfg.SNIHost
		if sni == "" {
			sni = cfg.Host
		}
		tlsCfg := &tls.Config{ServerName: sni, InsecureSkipVerify: true} //nolint:gosec
		tlsConn := tls.Client(raw, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}
		return tlsConn, nil

	case ModeSNIHTTPProxy:
		proxyAddr := fmt.Sprintf("%s:%d", cfg.HTTPProxyHost, cfg.HTTPProxyPort)
		raw, err := dnsx.New().DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("http proxy dial: %w", err)
		}

		// Build CONNECT request — optionally with payload injection
		var connectReq string
		if cfg.PayloadEnabled && cfg.Payload != "" {
			target := transport.Target{Host: cfg.Host, Port: cfg.Port}
			connectReq = substitutePayload(cfg.Payload, target)
			// Add payload opts
			_ = transport.WritePayload(raw, cfg.Payload, target, cfg.PayloadOpts)
		} else {
			connectReq = buildCONNECT(cfg.Host, cfg.Port)
			if _, err := raw.Write([]byte(connectReq)); err != nil {
				raw.Close()
				return nil, fmt.Errorf("proxy CONNECT write: %w", err)
			}
		}

		// Drain proxy/CDN acknowledgement
		raw = drainPayloadResponse(ctx, raw)

		sni := cfg.SNIHost
		if sni == "" {
			sni = cfg.Host
		}
		tlsCfg := &tls.Config{ServerName: sni, InsecureSkipVerify: true} //nolint:gosec
		tlsConn := tls.Client(raw, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}
		return tlsConn, nil

	default:
		return nil, fmt.Errorf("unknown transport mode: %s", cfg.Mode)
	}
}

// buildCONNECT assembles a standard HTTP CONNECT request.
func buildCONNECT(host string, port int) string {
	target := fmt.Sprintf("%s:%d", host, port)
	return fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", target, target)
}

// substitutePayload replaces template variables in a payload string.
func substitutePayload(payload string, target transport.Target) string {
	p := payload
	p = strings.ReplaceAll(p, "[host]", target.Host)
	p = strings.ReplaceAll(p, "[port]", fmt.Sprintf("%d", target.Port))
	p = strings.ReplaceAll(p, "[host_port]", fmt.Sprintf("%s:%d", target.Host, target.Port))
	p = strings.ReplaceAll(p, "[crlf]", "\r\n")
	p = strings.ReplaceAll(p, "[cr]", "\r")
	p = strings.ReplaceAll(p, "[lf]", "\n")
	return p
}

const payloadDrainTimeout = 12 * time.Second

// drainPayloadResponse consumes HTTP acknowledgement headers after a payload
// injection and returns a net.Conn that replays any tunnel bytes already read
// past the headers.
func drainPayloadResponse(ctx context.Context, conn net.Conn) net.Conn {
	deadline := time.Now().Add(payloadDrainTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	br := bufio.NewReader(conn)
	peek, err := br.Peek(5)
	if err != nil || string(peek) != "HTTP/" {
		return wrapBuffered(conn, br)
	}

	first := true
	for {
		if !first {
			if br.Buffered() < 5 {
				break
			}
			ahead, _ := br.Peek(5)
			if string(ahead) != "HTTP/" {
				break
			}
		}
		status, herr := readHTTPHeaderBlock(br)
		if status != "" {
			log.Printf("[payload] server response: %s", status)
		}
		if herr != nil {
			break
		}
		first = false
	}
	return wrapBuffered(conn, br)
}

// readHTTPHeaderBlock reads a status line plus headers up to the blank line.
func readHTTPHeaderBlock(br *bufio.Reader) (string, error) {
	status := ""
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			if status == "" {
				status = trimmed
			}
			if trimmed == "" {
				return status, nil
			}
		}
		if err != nil {
			return status, err
		}
	}
}

// awaitSSHBanner reads after a payload injection, discarding HTTP responses
// until the SSH identification banner ("SSH-…") appears. Returns a net.Conn
// with the banner preserved in the buffer.
func awaitSSHBanner(ctx context.Context, conn net.Conn) (net.Conn, error) {
	deadline := time.Now().Add(payloadDrainTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	br := bufio.NewReader(conn)
	var lastStatus string
	for {
		peek, err := br.Peek(4)
		if err != nil {
			if lastStatus != "" {
				return nil, fmt.Errorf("server rejected upgrade (%s) — no SSH banner", lastStatus)
			}
			return nil, fmt.Errorf("payload response: %w", err)
		}
		switch string(peek) {
		case "SSH-":
			return wrapBuffered(conn, br), nil
		case "HTTP":
			status, herr := readHTTPHeaderBlock(br)
			if status != "" {
				lastStatus = status
				log.Printf("[payload] server response: %s", status)
			}
			if herr != nil {
				return nil, fmt.Errorf("server response %q ended early: %w", status, herr)
			}
		default:
			return wrapBuffered(conn, br), nil
		}
	}
}

// wrapBuffered returns a net.Conn that replays bytes already buffered in br.
func wrapBuffered(conn net.Conn, br *bufio.Reader) net.Conn {
	n := br.Buffered()
	if n <= 0 {
		return conn
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		return conn
	}
	return &prefixConn{Conn: conn, prefix: buf}
}

// prefixConn replays a buffered prefix before reading from the underlying conn.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// DialTCP opens a direct SSH tunnel to the given destination.
func (c *Client) DialTCP(ctx context.Context, network, addr string) (net.Conn, error) {
	return c.sshConn.DialContext(ctx, network, addr)
}

// Close shuts down the SSH client.
func (c *Client) Close() {
	c.cancel()
	c.sshConn.Close()
}

// Wait blocks until the SSH connection is closed.
func (c *Client) Wait() error {
	return c.sshConn.Wait()
}
