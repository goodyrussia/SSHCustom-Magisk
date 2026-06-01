# Code Review — SSHCustom-Magisk v3.1.0 (pt1)

**Date:** 2026-06-01
**Files Reviewed:**
- `cmd/sshcustomd/main.go` (351 lines)
- `internal/ssh/client.go` (392 lines)
- `internal/proxy/socks5.go` (150 lines)

---

## 1. cmd/sshcustomd/main.go

### B1 — Unchecked `os.MkdirAll` error *(line 78)*
```go
os.MkdirAll(workDir, 0700)
```
`os.MkdirAll` can fail (permission denied, read-only filesystem, etc.). The returned error is discarded. If the directory cannot be created, later operations (WebUI serving from disk, config writes) will fail with confusing errors far from the root cause.

**Fix:**
```go
if err := os.MkdirAll(workDir, 0700); err != nil {
    log.Fatalf("[main] create workdir %s: %v", workDir, err)
}
```

### B2 — Race condition: `DaemonState` fields written without synchronisation *(lines 147–148, 275, 283–284, 291–293, 301–302, 339–340)*

`api.DaemonState` is a plain struct with exported fields. It is mutated by at least **four** goroutines concurrently:
- `runTunnel` (writes `Connected`, `TunnelStart`, `LastError`)
- `stopTunnel` (writes `Connected`, `LastError`)
- `metricsLoop` (writes `MemMB`, `CPUPct`, `ActiveConns`)
- API handler `handleTunnelStart` / `handleProfiles` (writes `CurrentProfile`)
- API handler `snapshot()` (reads all fields for `GET /status`)
- `handleTunnelStart` inside the API package also writes `s.State.CurrentProfile`

There is no mutex protecting `Connected`, `TunnelStart`, `LastError`, `MemMB`, `CPUPct`, or `CurrentProfile`. This is a data race detectable by `go test -race`.

`ActiveConns` uses `atomic.StoreInt32`/`atomic.LoadInt32` consistently — that one field is safe.

**Fix:** Wrap all `DaemonState` mutations behind a mutex, or convert all fields to `atomic` types. Simplest approach: add `sync.Mutex` to `DaemonState` and acquire it in `snapshot()` and every writer. Alternatively, use a dedicated `atomic.Pointer[DaemonState]` with copy-on-write.

### B3 — `metricsLoop` context never cancelled *(line 201)*
```go
go metricsLoop(context.Background(), st, &sshClient)
```
`metricsLoop` is started with `context.Background()`, which is never cancelled. When the daemon receives SIGTERM/SIGINT and `main()` returns, the process exits, so the goroutine dies with it. However, on SIGHUP reload it keeps running. If the daemon were ever refactored to not exit `main()` immediately (e.g., graceful restart), this would leak a goroutine and ticker.

**Fix:** Create a cancellable context for metrics and cancel it in the shutdown path:
```go
metricsCtx, metricsCancel := context.WithCancel(context.Background())
go metricsLoop(metricsCtx, st, &sshClient)
// In shutdown:
metricsCancel()
```

### B4 — SIGHUP handler does not re-apply profile merge *(lines 209–217)*
```go
case syscall.SIGHUP:
    newCfg, err := config.LoadConfig(*cfgPath)
    // ...
    atomicCfg.Set(newCfg)
```
When SIGHUP triggers a config reload, only the base `config.json` is reloaded. If a profile was previously active (merged via `ConfigFromProfile`), the reload **wipes out** all profile-derived settings (SSH host, SNI host, payload settings, etc.). The profile is silently deactivated.

**Fix:** After loading `newCfg`, re-apply the current profile if one is selected:
```go
if st.CurrentProfile != "" {
    pf, _ := config.LoadProfiles(*profilesPath)
    if profile := config.GetCurrentProfile(pf); profile != nil {
        newCfg = config.ConfigFromProfile(newCfg, profile)
    }
}
atomicCfg.Set(newCfg)
```

### B5 — Stale `runTunnel` goroutine can Store(nil) over a new Client *(lines 94–103, 300)*

When `startTunnel` is called while a tunnel is already running:
1. `tunnelCancel()` cancels the old context (line 100).
2. A new context is created and a new `runTunnel` goroutine is launched (line 128).
3. When the new goroutine connects, it calls `clientPtr.Store(c)` (line 290).
4. **Meanwhile**, the old `runTunnel` goroutine eventually detects the cancelled context, calls `c.Wait()` (which returns immediately with an error because keepalive/sshConn was closed), then calls `clientPtr.Store(nil)` (line 300) — **wiping the new Client pointer**.

The `tunnelMu` mutex protects `tunnelCancel` assignment but not the `sshClient` pointer. The 100ms sleep on line 102 is a heuristic, not a guarantee.

**Fix:** Give each `runTunnel` invocation a unique token (e.g., a generation counter) and only allow `Store(nil)` if the pointer still matches the goroutine's own Client. Alternatively, `startTunnel` should explicitly `sshClient.Store(nil)` before calling `tunnelCancel()` and wait for the old goroutine to finish (via a `sync.WaitGroup` or done channel).

### B6 — `stopTunnel` does not wait for the tunnel goroutine to finish *(lines 132–150)*

`stopTunnel` cancels the context and closes the SSH client, but it does not wait for the `runTunnel` goroutine to actually return. If `stopTunnel` is followed immediately by `startTunnel`, the old goroutine may still be running and can interfere (see B5 above).

**Fix:** Use a `sync.WaitGroup` or a `<-done` channel per tunnel goroutine.

### B7 — Fragile 100ms sleep in `startTunnel` *(line 102)*
```go
time.Sleep(100 * time.Millisecond)
```
This assumes the old goroutine will notice cancellation and clean up within 100ms. On a slow device or under load, this may not be enough. This is not a correctness guarantee.

**Fix:** Remove the sleep and use proper synchronisation (see B5/B6).

### B8 — SIGHUP: `newCfg` shadows but may not be merged with profile *(line 211–217)*

If `st.CurrentProfile` is set, `atomicCfg.Set(newCfg)` on line 215 overwrites the profile-merged config with the base config. See B4.

---

## 2. internal/ssh/client.go

### B9 — `PayloadOpts` never populated from config/profile *(lines 44, 258–272 of main.go)*

`ConnectConfig.PayloadOpts` is of type `transport.PayloadOpts` and contains fields `InjectionType`, `Method`, `FrontQuery`, `BackQuery`, `DualConnect`, `Split`, `UserAgent`, `ExtraHeaders`.

In `main.go` lines 258–272, `ConnectConfig` is constructed from `*config.Config`, but `PayloadOpts` is **never set**. It remains at zero value.

Meanwhile, `config.Profile` *does* have these fields (`PayloadInjectionType`, `PayloadMethod`, `PayloadFrontQuery`, `PayloadBackQuery`, `PayloadDualConnect`, `PayloadSplit`), but `config.ConfigFromProfile()` (in `config/config.go` lines 173–206) **does not copy them into the Config struct** — because `config.Config` doesn't even have those fields.

**Result:** All payload injection options (front/back query, dual connect, split, custom method, user-agent) configured in a profile are **completely ignored**. Only `PayloadEnabled` and the raw `Payload` template string are used.

**Fix:**
1. Add the missing fields to `config.Config`:
   ```go
   PayloadInjectionType string `json:"payload_injection_type,omitempty"`
   PayloadMethod        string `json:"payload_method,omitempty"`
   PayloadFrontQuery    bool   `json:"payload_front_query"`
   PayloadBackQuery     bool   `json:"payload_back_query"`
   PayloadDualConnect   bool   `json:"payload_dual_connect"`
   PayloadSplit         bool   `json:"payload_split"`
   ```
2. Copy them in `ConfigFromProfile()`.
3. Map them to `transport.PayloadOpts` in `main.go` when building `ConnectConfig`.

### B10 — Dead code: `connectReq` built but never written *(line 200)*

In `dialTransport`, `ModeSNIHTTPProxy` with payload enabled:
```go
connectReq = substitutePayload(cfg.Payload, target)  // line 200 — assigned
_ = transport.WritePayload(raw, cfg.Payload, target, cfg.PayloadOpts)  // line 202
```
`connectReq` is computed but **never written to the connection**. The actual write is done by `WritePayload`. The variable is dead code.

**Fix:** Remove the unused `connectReq` assignment if `WritePayload` handles everything, or restructure to use `connectReq` directly.

### B11 — `drainPayloadResponse` not called in `ModeSNIHTTPProxy` non-payload path *(lines 196–212)*

Wait — actually it IS called on line 212, which is outside the if/else block. Both paths converge. This is **correct**. Strike this.

**Revised:** This is **not a bug** — `drainPayloadResponse` at line 212 runs for both branches. ✅

### B12 — `keepAlive` `SendRequest` has no timeout *(line 127)*
```go
_, _, err := c.sshConn.SendRequest("keepalive@openssh.com", true, nil)
```
`SendRequest` blocks until:
- The server replies, OR
- The underlying TCP connection times out (which may be never if no deadline is set), OR
- The SSH connection is closed.

If the server hangs (e.g., network partition with no RST), this call blocks **forever**. The keepalive ticker keeps firing but each call stacks up blocked goroutines.

**Fix:** Use `SendRequest` with a context or set a deadline before the call:
```go
// Or better: use a separate request-specific timeout
reqCtx, cancel := context.WithTimeout(c.ctx, interval/2)
_, _, err := c.sshConn.SendRequestWithContext(reqCtx, "keepalive@openssh.com", true, nil)
cancel()
```
(Note: `xssh.Client.SendRequestWithContext` may not exist in all versions of `golang.org/x/crypto/ssh`. An alternative is to set a connection-level deadline: `c.sshConn.Conn.SetDeadline(...)` but that interferes with tunnel traffic.)

### B13 — Empty password creates zero-length auth without clear error *(line 92)*
```go
Auth: []xssh.AuthMethod{xssh.Password(cfg.Password)},
```
If `cfg.Password` is empty, this sends a zero-length password authentication attempt. The SSH server will reject it, but the error message won't clearly indicate "password is empty". This makes debugging harder.

**Fix:** Validate before dialing:
```go
if cfg.Password == "" {
    return nil, fmt.Errorf("ssh: password must not be empty")
}
```

### B14 — No validation for `ModeSNIHTTPProxy` when proxy host/port are empty *(lines 189–190)*
```go
case ModeSNIHTTPProxy:
    proxyAddr := fmt.Sprintf("%s:%d", cfg.HTTPProxyHost, cfg.HTTPProxyPort)
```
If `cfg.HTTPProxyHost` is `""` and `cfg.HTTPProxyPort` is `0`, `proxyAddr` becomes `":0"`, which will either dial localhost:0 (failing) or cause confusing errors.

**Fix:** Validate early:
```go
if cfg.HTTPProxyHost == "" || cfg.HTTPProxyPort == 0 {
    return nil, fmt.Errorf("sni_http_proxy mode requires http_proxy_host and http_proxy_port")
}
```

### B15 — `wrapBuffered` silently drops data on `io.ReadFull` failure *(lines 356–358)*
```go
buf := make([]byte, n)
if _, err := io.ReadFull(br, buf); err != nil {
    return conn
}
```
If `io.ReadFull` fails (e.g., the `bufio.Reader` was partially consumed or has an internal error), the buffered bytes are **silently lost**. This could mean the SSH banner bytes are never replayed, causing the SSH handshake to fail with a cryptic error.

**Fix:** At minimum, log the error. Better: return an error up the call stack. Since `wrapBuffered` returns a `net.Conn`, the error could be surfaced via a wrapper that returns the error on the first `Read()`.

### B16 — `readHTTPHeaderBlock` has no line length limit *(lines 292–309)*

If the server sends a very long HTTP header (e.g., `Set-Cookie: ...` with 10KB), `br.ReadString('\n')` will allocate accordingly. A malicious or misconfigured proxy could cause memory exhaustion.

**Fix:** Use a `bufio.Scanner` or implement a maximum line length check.

### B17 — `ModeSNI` and `ModeSNIHTTPProxy` use `InsecureSkipVerify: true` *(lines 181, 218)*

This disables TLS certificate verification. While intentional (carrier/CDN TLS interception), it makes the connection vulnerable to MITM attacks. This is a deliberate design choice, but should be prominently documented and ideally configurable.

*(Informational — not a bug per the design intent, but a significant security concern.)*

---

## 3. internal/proxy/socks5.go

### B18 — `io.ReadFull` errors IGNORED *(lines 79, 83, 85, 89, 97)*
```go
case 1: // IPv4
    addr := make([]byte, 4)
    io.ReadFull(conn, addr)        // ← error ignored
    host = net.IP(addr).String()
case 3: // Domain name
    lenb := make([]byte, 1)
    io.ReadFull(conn, lenb)        // ← error ignored
    dom := make([]byte, lenb[0])
    io.ReadFull(conn, dom)         // ← error ignored
    host = string(dom)
case 4: // IPv6
    addr := make([]byte, 16)
    io.ReadFull(conn, addr)        // ← error ignored
    host = "[" + net.IP(addr).String() + "]"
```
...and:
```go
portBuf := make([]byte, 2)
io.ReadFull(conn, portBuf)        // ← error ignored (line 97)
```

`io.ReadFull` is called **5 times** and the returned error is discarded every time. If the client disconnects mid-handshake:
- `addr` stays all-zero → host becomes `"0.0.0.0"`
- `lenb` stays `{0}` → `dom` is 0 bytes → host is `""`
- `portBuf` stays `{0,0}` → port is `0`
- The SOCKS5 proxy will attempt to SSH-tunnel to `":0"` or `"0.0.0.0:0"` and fail with a confusing error.

**Fix:** Check every `io.ReadFull` error and return on failure:
```go
if _, err := io.ReadFull(conn, addr); err != nil {
    return
}
```

### B19 — No upper bound on `nmethods` *(line 60)*
```go
nmethods := int(hdr[1])
methods := make([]byte, nmethods)
if _, err := io.ReadFull(conn, methods); err != nil {
    return
}
```
A malicious client can send `nmethods = 255`, causing allocation of 255 bytes and a read of 255 bytes. While not catastrophic, repeated connections could waste memory. Per RFC 1928, nmethods is 1–255.

**Fix:** Cap at a reasonable value:
```go
if nmethods < 1 || nmethods > 16 {
    return
}
```

### B20 — `Write` errors silently ignored *(lines 66, 71, 92, 105, 111, 120)*

Every `conn.Write()` in the SOCKS5 handler discards the returned error:
```go
conn.Write([]byte{5, 0})                                          // line 66
conn.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})                // line 71
conn.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})                // line 92
conn.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})                // line 105
conn.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})                // line 111
conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})                // line 120
```
If the client has already disconnected, these writes fail silently. This isn't a correctness issue (the connection is dead anyway), but it's poor hygiene and makes debugging harder.

**Fix:** Check and log write errors at debug level.

### B21 — `relay` double-closes connections *(lines 115 + 142, 52 + 142)*

In `handle()`:
```go
defer conn.Close()       // line 52 — will close conn
// ...
defer remote.Close()     // line 114 — will close remote
// ...
relay(conn, remote)      // line 123
```

In `relay()`:
```go
cp := func(dst, src net.Conn) {
    // ...
    if hc, ok := dst.(halfCloser); ok {
        hc.CloseWrite()
    } else {
        dst.Close()      // ← closes conn or remote
    }
}
```

So `conn` is closed by `relay`'s `cp` goroutine AND by `handle`'s deferred `conn.Close()`. Same for `remote`. While Go's `net.Conn` implementations handle double-close gracefully (the second `Close()` returns an error), it's still a logic smell.

**Fix:** Don't defer-close in `handle` since `relay` handles cleanup:
```go
// Remove: defer conn.Close()
// Remove: defer remote.Close()
// relay handles all cleanup
relay(conn, remote)
```

### B22 — `io.CopyBuffer` errors silently ignored in `relay` *(lines 130–133)*
```go
cp := func(dst, src net.Conn) {
    defer func() { done <- struct{}{} }()
    buf := make([]byte, copyBufSize)
    io.CopyBuffer(dst, src, buf)
    // ...
}
```
If `io.CopyBuffer` fails (e.g., network error), the error is discarded. The relay terminates silently, and the caller never knows why. This makes diagnosing connection issues harder.

**Fix:** At minimum, log the error:
```go
n, err := io.CopyBuffer(dst, src, buf)
if err != nil && err != io.EOF {
    log.Printf("[socks5] relay error: %v", err)
}
```

### B23 — No error response when SOCKS version != 5 *(lines 57–58)*
```go
if _, err := io.ReadFull(conn, hdr); err != nil || hdr[0] != 5 {
    return
}
```
When the version byte is not 5, the connection just closes without sending a SOCKS5 error reply. A compliant SOCKS5 client may hang waiting for the version negotiation response.

**Fix:** Send an error before returning:
```go
if err != nil {
    return
}
if hdr[0] != 5 {
    conn.Write([]byte{5, 0xFF}) // version 5, no acceptable methods
    return
}
```

---

## Summary

| # | Severity | File | Line(s) | Category | Description |
|---|----------|------|---------|----------|-------------|
| B1 | High | main.go | 78 | Missing error check | `os.MkdirAll` error ignored |
| B2 | **Critical** | main.go | 147–148,275,283–284,291–293,301–302,339–340 | Race condition | `DaemonState` fields written from multiple goroutines without synchronisation |
| B3 | Medium | main.go | 201 | Goroutine leak | `metricsLoop` context never cancelled |
| B4 | High | main.go | 215 | Logic error | SIGHUP reload does not re-apply profile merge |
| B5 | **Critical** | main.go | 94–103, 300 | Race condition | Old `runTunnel` can `Store(nil)` over new Client |
| B6 | High | main.go | 132–150 | Logic error | `stopTunnel` doesn't wait for goroutine to finish |
| B7 | Medium | main.go | 102 | Fragile code | 100ms sleep as synchronisation heuristic |
| B8 | High | main.go | 211–217 | Logic error | SIGHUP drops profile-merged config (same as B4) |
| B9 | **Critical** | client.go | 44, main.go:258–272 | API contract / missing mapping | `PayloadOpts` never populated; all injection options ignored |
| B10 | Low | client.go | 200 | Dead code | `connectReq` built but never written |
| B12 | High | client.go | 127 | Missing timeout | keepalive `SendRequest` can block forever |
| B13 | Medium | client.go | 92 | Missing validation | Empty password gives unclear error |
| B14 | High | client.go | 189–190 | Missing validation | No check for empty proxy host/port in SNIHTTPProxy mode |
| B15 | High | client.go | 356–358 | Data loss | `wrapBuffered` silently drops buffered bytes on `io.ReadFull` failure |
| B16 | Low | client.go | 292–309 | Resource leak | No line length limit in HTTP header reader |
| B17 | Warn | client.go | 181, 218 | Security | `InsecureSkipVerify: true` disables TLS verification |
| B18 | **Critical** | socks5.go | 79, 83, 85, 89, 97 | Missing error check | `io.ReadFull` errors ignored → garbage host/port |
| B19 | Medium | socks5.go | 60 | Missing validation | No upper bound on nmethods |
| B20 | Low | socks5.go | 66, 71, 92, 105, 111, 120 | Missing error check | Write errors silently ignored |
| B21 | Low | socks5.go | 52, 114, 142 | Logic issue | Double-close of conn and remote |
| B22 | Medium | socks5.go | 130–133 | Missing error check | `io.CopyBuffer` errors silently discarded in relay |
| B23 | Medium | socks5.go | 57–58 | Protocol compliance | No error reply when SOCKS version != 5 |

**Total: 23 findings (8 Critical/High, 8 Medium, 4 Low, 1 Warning, 2 Informational duplicates merged)**

### Top priorities:
1. **B2 / B5 — Race conditions on DaemonState and sshClient pointer** — these can cause crashes or incorrect status reporting under concurrent tunnel start/stop.
2. **B9 — PayloadOpts never populated** — silently breaks all payload injection strategies beyond basic template substitution.
3. **B18 — io.ReadFull errors ignored in SOCKS5** — causes proxy to attempt connections to garbage addresses.
4. **B4/B8 — SIGHUP drops profile** — config reload silently removes active profile settings.
5. **B15 — wrapBuffered data loss** — can cause mysterious SSH handshake failures after payload injection.
