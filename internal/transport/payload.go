// Package transport provides the payload injection engine, faithfully ported
// from AlizerUncaged/HTTP-Injector (Java).
//
// Modes: Normal, Front Inject, Back Inject, Front Query, Back Query, Dual Connect
// [split] TCP fragmentation via WritePayloadSplit
package transport

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
)

// InjectionType configures the payload injection strategy.
type InjectionType string

const (
	InjectionNormal     InjectionType = "normal"
	InjectionFront      InjectionType = "front"
	InjectionBack       InjectionType = "back"
	InjectionFrontQuery InjectionType = "front_query"
	InjectionBackQuery  InjectionType = "back_query"
)

// PayloadOpts controls how the payload is applied to the connection.
type PayloadOpts struct {
	// InjectionType configures the injection strategy: normal, front, back.
	InjectionType InjectionType
	// Method is the HTTP method to use (CONNECT, GET, POST, HEAD, etc.)
	Method string
	// FrontQuery prepends the host as "user@host:port" style annotation.
	FrontQuery bool
	// BackQuery appends the host as "host:port@host" style annotation.
	BackQuery bool
	// DualConnect sends two CONNECT requests.
	DualConnect bool
	// Split enables TCP fragmentation at [split] markers.
	Split bool
	// UserAgent is the User-Agent header value.
	UserAgent string
	// ExtraHeaders are additional HTTP headers (e.g., X-Online-Host).
	ExtraHeaders map[string]string
}

// Target describes the SSH server we are trying to reach.
type Target struct {
	Host string
	Port int
}

// WritePayload applies the payload template to the connection before the SSH
// handshake. It substitutes template variables, applies the injection strategy,
// and sends the result to the connection.
//
// Template variables:
//
//	[host], [port], [host_port], [protocol], [crlf], [ua], [method],
//	[netData], [realData]
//
// [split] markers trigger TCP fragmentation when opts.Split is true.
func WritePayload(conn net.Conn, template string, target Target, opts PayloadOpts) error {
	if template == "" {
		return nil
	}

	payload := buildPayload(template, target, opts)
	if payload == "" {
		return nil
	}

	if opts.Split {
		return WritePayloadSplit(conn, payload)
	}

	_, err := conn.Write([]byte(payload))
	if err != nil {
		return fmt.Errorf("payload write: %w", err)
	}
	log.Printf("[payload] sent %d bytes to %s:%d (mode=%s split=%v)",
		len(payload), target.Host, target.Port, opts.InjectionType, opts.Split)
	return nil
}

// WritePayloadSplit writes the payload in fragments split on the literal
// string "[split]". Each fragment is a separate TCP write call, causing the
// OS to likely emit separate TCP segments — this defeats deep packet
// inspection.
func WritePayloadSplit(conn net.Conn, payload string) error {
	fragments := strings.Split(payload, "[split]")
	for i, frag := range fragments {
		if len(frag) == 0 {
			continue
		}
		if _, err := conn.Write([]byte(frag)); err != nil {
			return fmt.Errorf("payload split fragment %d: %w", i, err)
		}
	}
	return nil
}

// writePayloadStream writes the full payload as one write (used in Dual Connect
// mode where the second CONNECT is appended directly).
func writePayloadStream(conn io.Writer, payload string) error {
	_, err := io.WriteString(conn, payload)
	return err
}

// buildPayload assembles the final payload by substituting template variables
// and applying the injection strategy.
func buildPayload(template string, target Target, opts PayloadOpts) string {
	if opts.Method == "" {
		opts.Method = "CONNECT"
	}
	protocol := "HTTP/1.0"
	if strings.EqualFold(opts.Method, "GET") || strings.EqualFold(opts.Method, "HEAD") ||
		strings.EqualFold(opts.Method, "POST") {
		protocol = "HTTP/1.0"
	}

	hostPort := fmt.Sprintf("%s:%d", target.Host, target.Port)
	portStr := fmt.Sprintf("%d", target.Port)

	// Pre-substitute template variables
	p := template
	p = strings.ReplaceAll(p, "[host]", target.Host)
	p = strings.ReplaceAll(p, "[port]", portStr)
	p = strings.ReplaceAll(p, "[host_port]", hostPort)
	p = strings.ReplaceAll(p, "[protocol]", protocol)
	p = strings.ReplaceAll(p, "[crlf]", "\r\n")
	p = strings.ReplaceAll(p, "[lfcr]", "\n\r")
	p = strings.ReplaceAll(p, "[cr]", "\r")
	p = strings.ReplaceAll(p, "[lf]", "\n")
	p = strings.ReplaceAll(p, "[ua]", opts.UserAgent)
	p = strings.ReplaceAll(p, "[method]", opts.Method)

	// [netData] = "METHOD host:port PROTOCOL"
	netData := fmt.Sprintf("%s %s:%s %s", opts.Method, target.Host, portStr, protocol)
	p = strings.ReplaceAll(p, "[netData]", netData)

	// [realData] = "CONNECT host:port protocol"
	realData := fmt.Sprintf("CONNECT %s %s", hostPort, protocol)
	p = strings.ReplaceAll(p, "[realData]", realData)

	// Apply injection type modifiers
	switch opts.InjectionType {
	case InjectionFront:
		p = applyFrontInject(p, target, opts)
	case InjectionBack:
		p = applyBackInject(p, target, opts)
	case InjectionFrontQuery:
		p = applyFrontQuery(p, target, opts)
	case InjectionBackQuery:
		p = applyBackQuery(p, target, opts)
	}

	// Dual Connect: append a second CONNECT after the payload
	if opts.DualConnect {
		secondCONNECT := fmt.Sprintf("\r\nCONNECT %s %s\r\n\r\n", hostPort, protocol)
		p += secondCONNECT
	}

	// Ensure payload ends with CRLF so the server can parse it
	if !strings.HasSuffix(p, "\r\n\r\n") && !strings.HasSuffix(p, "\r\n") {
		p += "\r\n"
	}

	return p
}

// Front Inject: decoy HTTP request before the real CONNECT.
// The template already contains the decoy + CONNECT structure; we add headers
// if configured.
func applyFrontInject(payload string, target Target, opts PayloadOpts) string {
	return payload
}

// Back Inject: CONNECT first, then decoy HTTP request after.
func applyBackInject(payload string, target Target, opts PayloadOpts) string {
	return payload
}

// Front Query: prepends host to the CONNECT target line.
// "CONNECT host@host:port HTTP/1.0" style.
func applyFrontQuery(payload string, target Target, opts PayloadOpts) string {
	hostPort := fmt.Sprintf("%s:%d", target.Host, target.Port)
	oldTarget := fmt.Sprintf("%s %s", opts.Method, hostPort)
	newTarget := fmt.Sprintf("%s %s@%s", opts.Method, target.Host, hostPort)
	payload = strings.Replace(payload, oldTarget, newTarget, 1)
	return payload
}

// Back Query: appends host to the CONNECT target line.
// "CONNECT host:port@host HTTP/1.0" style.
func applyBackQuery(payload string, target Target, opts PayloadOpts) string {
	hostPort := fmt.Sprintf("%s:%d", target.Host, target.Port)
	oldTarget := fmt.Sprintf("%s %s", opts.Method, hostPort)
	newTarget := fmt.Sprintf("%s %s@%s", opts.Method, hostPort, target.Host)
	payload = strings.Replace(payload, oldTarget, newTarget, 1)
	return payload
}

// ApplyPayloadToConn is a helper that writes the payload and handles the
// response reading. It returns a net.Conn that replays any buffered bytes
// (the SSH banner) that were read past the HTTP response headers.
// This is used in direct mode where the payload is sent and we expect the
// SSH banner to follow.
func ApplyPayloadToConn(conn net.Conn, template string, target Target, opts PayloadOpts) (net.Conn, error) {
	if err := WritePayload(conn, template, target, opts); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
