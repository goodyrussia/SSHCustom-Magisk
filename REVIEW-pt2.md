# Code Review — Part 2: Line-by-Line Analysis

**Date:** 2026-06-01
**Files reviewed:**
1. `internal/transport/dialer.go` (105 lines)
2. `internal/transport/payload.go` (215 lines)
3. `internal/api/handlers.go` (441 lines)
4. `internal/api/contracts.go` (80 lines)

**Cross-referenced:**
- `internal/config/config.go`
- `internal/dnsx/dnsx.go`
- `internal/ssh/client.go`
- `internal/version/version.go`

---

## 1. `internal/transport/dialer.go`

### BUG-1.1 — Direct dial ignores Timeout (line 27)

```go
return dnsx.New().DialContext(ctx, network, addr)
```

The `Dialer.Timeout` field is only applied in the proxy path (line 34–36). In the direct path, the caller's context is used as-is, with no timeout enforcement. If the caller passes `context.Background()`, the dial will block indefinitely.

**Fix:** Wrap the context with the configured timeout, same as the proxy path:

```go
timeout := d.Timeout
if timeout == 0 {
    timeout = 25 * time.Second
}
dialCtx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()
return dnsx.New().DialContext(dialCtx, network, addr)
```

### BUG-1.2 — Slice bounds panic on truncated proxy response (line 70)

```go
return nil, fmt.Errorf("proxy CONNECT rejected: %s", status[:16])
```

If `len(status)` is between 12 and 15 (e.g., `"HTTP/1.0 403"` = 12 bytes), then `status[:16]` panics. The len check on line 63 only rejects `< 12`, not `< 16`.

**Fix:**

```go
end := len(status)
if end > 40 { end = 40 }
return nil, fmt.Errorf("proxy CONNECT rejected: %s", status[:end])
```

### BUG-1.3 — Dead code: `resolveHost` (lines 93–105)

The function `resolveHost` is never called anywhere in the transport package. `dnsx.Resolver.DialContext` does its own resolution internally (dnsx.go:50–81), and `dnsx.Resolver.Lookup` is the public API.

**Fix:** Remove the function or refactor `DialContext` to use it for resolving the target host before dialing the proxy (the proxy dial currently passes `targetAddr` directly to `dnsx.New().DialContext` which handles resolution).

### BUG-1.4 — Inefficient byte-by-byte header drain (lines 74–87)

The drain loop reads one byte at a time with `make([]byte, 1)` looking for `\r\n\r\n`. This is extremely slow and fragile. The `crlfCount` heuristic counts any `\r` or `\n` independently, so `\r\r\n\n` would count as 4 and exit early.

**Fix:** Use `bufio.Reader` to read lines until a blank line is encountered.

### BUG-1.5 — Dialer struct likely dead code

Neither the SSH client (`internal/ssh/client.go`) nor the API handlers reference `transport.Dialer`. The SSH client uses `dnsx.New().DialContext` directly for all three modes. The `Dialer` type appears to be a legacy artifact from an earlier architecture or a parallel code path that was never integrated.

**Fix:** Either integrate the `Dialer` into the SSH client's transport layer or remove it.

---

## 2. `internal/transport/payload.go`

### BUG-2.1 — `applyFrontInject` and `applyBackInject` are no-ops (lines 175–181)

```go
func applyFrontInject(payload string, target Target, opts PayloadOpts) string {
    return payload
}
func applyBackInject(payload string, target Target, opts PayloadOpts) string {
    return payload
}
```

These are the core injection strategies for Front/Back modes. The comment on `applyFrontInject` says **"we add headers if configured"** — but `opts.ExtraHeaders` is never read. The functions accept the `opts` parameter but discard it entirely. Any profile configured with `"front"` or `"back"` injection type will have zero HTTP header injection, making those modes non-functional.

**Fix:** Implement actual injection logic. For Front Inject: prepend a decoy HTTP request (using `opts.ExtraHeaders`, `opts.UserAgent`, `opts.Method`). For Back Inject: append after the CONNECT.

### BUG-2.2 — Protocol always `HTTP/1.0` regardless of method (lines 116–119)

```go
protocol := "HTTP/1.0"
if strings.EqualFold(opts.Method, "GET") || strings.EqualFold(opts.Method, "HEAD") ||
    strings.EqualFold(opts.Method, "POST") {
    protocol = "HTTP/1.0"
}
```

The `if` block is a tautology — `protocol` is already `"HTTP/1.0"`. For CONNECT (the default method), the protocol should be `"HTTP/1.1"` since HTTP CONNECT tunneling is an HTTP/1.1 feature. The GET/HEAD/POST methods should use `"HTTP/1.0"`, and CONNECT should use `"HTTP/1.1"`.

**Fix:**

```go
protocol := "HTTP/1.1"
if method := strings.ToUpper(opts.Method); method == "GET" || method == "HEAD" || method == "POST" {
    protocol = "HTTP/1.0"
}
```

### BUG-2.3 — Dead fields in `PayloadOpts`: `FrontQuery`, `BackQuery`, `ExtraHeaders` (lines 34, 36, 44)

These three fields are declared in `PayloadOpts` but **never read** anywhere in the codebase:

- `FrontQuery` (line 34) — never checked; injection type uses `InjectionType == "front_query"` instead
- `BackQuery` (line 36) — never checked; injection type uses `InjectionType == "back_query"` instead
- `ExtraHeaders` (line 44) — never referenced, despite `applyFrontInject`/`applyBackInject` comments saying "we add headers"

This is doubly problematic because the `config.Profile` struct has `PayloadFrontQuery` and `PayloadBackQuery` bools (`config.go` lines 51–52), and the API contracts propagate them (`contracts.go` lines 45–46) — but they are never wired into the payload injection engine.

**Fix:** Either remove the dead fields and simplify the code, or wire them into the injection logic (e.g., `FrontQuery` bool should trigger front-query behavior, `ExtraHeaders` should be injected into Front/Back mode payloads).

### BUG-2.4 — Dead code: `writePayloadStream` (lines 105–108)

```go
func writePayloadStream(conn io.Writer, payload string) error {
    _, err := io.WriteString(conn, payload)
    return err
}
```

Never called anywhere. The comment says "used in Dual Connect mode" but `buildPayload` appends the second CONNECT directly to the payload string (line 159–162). This function is dead.

**Fix:** Remove.

### BUG-2.5 — Dead code: `ApplyPayloadToConn` (lines 209–215)

```go
func ApplyPayloadToConn(conn net.Conn, template string, target Target, opts PayloadOpts) (net.Conn, error) {
    if err := WritePayload(conn, template, target, opts); err != nil {
        conn.Close()
        return nil, err
    }
    return conn, nil
}
```

Never called anywhere. The SSH client's direct mode calls `transport.WritePayload` directly and then calls `awaitSSHBanner` to drain the response (`ssh/client.go` lines 156–168). This wrapper is dead.

**Fix:** Remove, or use it in `ssh/client.go` to reduce duplication.

### BUG-2.6 — CRLF termination logic is broken (lines 165–167)

```go
if !strings.HasSuffix(p, "\r\n\r\n") && !strings.HasSuffix(p, "\r\n") {
    p += "\r\n"
}
```

**Bug:** If the payload ends with exactly one `\r\n`, the condition is `(false && true) = false`, so **no CRLF is added**. But an HTTP request/response header block MUST be terminated by a blank line (`\r\n\r\n`). A payload ending with just one `\r\n` will be malformed.

Example: payload `"GET / HTTP/1.0\r\nHost: example.com\r\n"` → ends with `\r\n` → condition is `!false && !true` → `true && false` → `false` → no CRLF added → payload stays unterminated.

**Fix:**

```go
if !strings.HasSuffix(p, "\r\n\r\n") {
    if strings.HasSuffix(p, "\r\n") {
        p += "\r\n"
    } else {
        p += "\r\n\r\n"
    }
}
```

Or more concisely:

```go
for !strings.HasSuffix(p, "\r\n\r\n") {
    p += "\r\n"
}
```

### BUG-2.7 — `[netData]` and `[realData]` identical when method is CONNECT (lines 139, 143)

```go
netData := fmt.Sprintf("%s %s:%s %s", opts.Method, target.Host, portStr, protocol)
// "CONNECT host:22 HTTP/1.0"

realData := fmt.Sprintf("CONNECT %s %s", hostPort, protocol)
// "CONNECT host:22 HTTP/1.0"
```

When `opts.Method` is `"CONNECT"` (the default), both variables evaluate to the same string. In the original HTTP Injector (Java), these serve different purposes:
- `[netData]` = full HTTP request including headers
- `[realData]` = just the CONNECT line

**Fix:** Align with the original semantics. `[netData]` should be a complete HTTP payload block (method line + headers + blank line). `[realData]` should remain as just `"CONNECT host:port PROTOCOL"`.

### DOC-BUG-2.8 — Undocumented template variables (lines 57–62 vs 132–134)

The doc comment for `WritePayload` lists: `[host]`, `[port]`, `[host_port]`, `[protocol]`, `[crlf]`, `[ua]`, `[method]`, `[netData]`, `[realData]`.

But the code also supports: `[lfcr]` (line 132), `[cr]` (line 133), `[lf]` (line 134).

**Fix:** Add `[lfcr]`, `[cr]`, `[lf]` to the doc comment.

---

## 3. `internal/api/contracts.go`

### BUG-3.1 — `ConfigResponse` missing 5 fields from `config.Config` (lines 19–28)

`config.Config` has these additional fields not in `ConfigResponse`:

| Field in `config.Config`      | JSON tag                     | Missing from `ConfigResponse`? |
|-------------------------------|------------------------------|-------------------------------|
| `SSHSNIHost`                  | `"ssh_sni_host,omitempty"`   | **YES**                       |
| `HTTPProxyHost`               | `"http_proxy_host,omitempty"`| **YES**                       |
| `HTTPProxyPort`               | `"http_proxy_port,omitempty"`| **YES**                       |
| `PayloadEnabled`              | `"payload_enabled"`          | **YES**                       |
| `Payload`                     | `"payload,omitempty"`        | **YES**                       |

**Impact:** GET `/api/v1/config` will never return these derived profile fields. If the WebUI displays a full config view, these fields will appear empty or defaulted.

**Fix:** Add the 5 fields to `ConfigResponse`:

```go
type ConfigResponse struct {
    // ... existing fields ...
    SSHSNIHost     string `json:"ssh_sni_host,omitempty"`
    HTTPProxyHost  string `json:"http_proxy_host,omitempty"`
    HTTPProxyPort  int    `json:"http_proxy_port,omitempty"`
    PayloadEnabled bool   `json:"payload_enabled"`
    Payload        string `json:"payload,omitempty"`
}
```

### NOTE-3.2 — `ProfileItem` and `ProfilesResponse` are correct

All 17 fields of `config.Profile` are faithfully mirrored in `ProfileItem`, with matching JSON tags. `ProfilesResponse` matches `config.ProfilesFile`. No issues.

### NOTE-3.3 — `StatusResponse`, `TunnelRequest`, `TunnelResponse`, `LatencyResponse` are correct

All structs match their handler usage. No JSON tag mismatches detected.

---

## 4. `internal/api/handlers.go`

### BUG-4.1 — Redundant/dead if-block in `handleStatus` (lines 111–113)

```go
if snap.Profile == "" {
    snap.Profile = s.State.CurrentProfile
}
```

`snap.Profile` is set from `s.CurrentProfile` in the `snapshot()` method (line 39). The field `s.CurrentProfile` in `DaemonState` IS `s.State.CurrentProfile` — they're the same field on the same struct. This branch can never be true. It is dead code.

**Fix:** Remove lines 111–113.

### BUG-4.2 — Config GET missing 5 fields (lines 129–138)

The handler populates `ConfigResponse` from `cfg` but `ConfigResponse` (see BUG-3.1) doesn't include `SSHSNIHost`, `HTTPProxyHost`, `HTTPProxyPort`, `PayloadEnabled`, `Payload`. These are silently dropped from the API response.

**Fix:** After adding the fields to `ConfigResponse`, populate them:

```go
resp := ConfigResponse{
    // ... existing fields ...
    SSHSNIHost:     cfg.SSHSNIHost,
    HTTPProxyHost:  cfg.HTTPProxyHost,
    HTTPProxyPort:  cfg.HTTPProxyPort,
    PayloadEnabled: cfg.PayloadEnabled,
    Payload:        cfg.Payload,
}
```

### BUG-4.3 — Config PUT silently drops 5 fields (lines 149–172)

The merge logic checks and copies only 8 fields:

```go
if cfg.SSHHost != ""      { merged.SSHHost = cfg.SSHHost }
if cfg.SSHPort > 0        { merged.SSHPort = cfg.SSHPort }
if cfg.SSHUser != ""      { merged.SSHUser = cfg.SSHUser }
if cfg.SSHPassword != ""  { merged.SSHPassword = cfg.SSHPassword }
if cfg.SSHMode != ""      { merged.SSHMode = cfg.SSHMode }
if cfg.SocksPort > 0      { merged.SocksPort = cfg.SocksPort }
if cfg.APIPort > 0        { merged.APIPort = cfg.APIPort }
if cfg.WorkDir != ""      { merged.WorkDir = cfg.WorkDir }
```

The following fields are decoded from the JSON body into `cfg` but **never merged into `merged`**:

- `cfg.SSHSNIHost`
- `cfg.HTTPProxyHost`
- `cfg.HTTPProxyPort`
- `cfg.PayloadEnabled`
- `cfg.Payload`

They are silently discarded. If the WebUI sends these fields in a PUT, they have no effect.

**Fix:** Add merge logic for these 5 fields:

```go
if cfg.SSHSNIHost != ""       { merged.SSHSNIHost = cfg.SSHSNIHost }
if cfg.HTTPProxyHost != ""    { merged.HTTPProxyHost = cfg.HTTPProxyHost }
if cfg.HTTPProxyPort > 0      { merged.HTTPProxyPort = cfg.HTTPProxyPort }
merged.PayloadEnabled = cfg.PayloadEnabled  // bool can't use zero-value check
if cfg.Payload != ""          { merged.Payload = cfg.Payload }
```

Note: `PayloadEnabled` is a `bool` — zero-value (`false`) is a valid setting. Either always apply it or use a pointer.

### BUG-4.4 — Unhandled decode error in `handleTunnelStart` (line 343)

```go
var req TunnelRequest
if r.Body != nil {
    json.NewDecoder(r.Body).Decode(&req)  // error NOT checked
}
```

If the JSON body is malformed, `req` will be zero-valued. The handler proceeds without diagnosing the problem. A garbled profile name or partial request body is silently treated as empty.

Additionally, `r.Body` is never `nil` in Go's `net/http` — it's always a non-nil `io.ReadCloser` (at minimum `http.NoBody`). The nil check is misleading and always true.

**Fix:**

```go
var req TunnelRequest
if r.ContentLength > 0 || r.Body != http.NoBody {
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, 400, "invalid JSON: "+err.Error())
        return
    }
}
```

### BUG-4.5 — `config.SaveProfiles` error not checked in `handleTunnelStart` (line 351)

```go
pf.Current = req.Profile
config.SaveProfiles(s.ProfilesPath, pf)  // error NOT checked
s.State.CurrentProfile = req.Profile
```

If `SaveProfiles` fails (e.g., disk full, permission denied), the in-memory state is updated but not persisted. On restart, the profile change is lost. This is a silent data-loss bug.

**Fix:** Check the error and at minimum log it, or revert the in-memory state if persistence fails.

### BUG-4.6 — `handleTunnelStart` updates state even when profile not found (lines 347–358)

```go
if req.Profile != "" {
    pf, err := config.LoadProfiles(s.ProfilesPath)
    if err == nil {
        pf.Current = req.Profile
        config.SaveProfiles(s.ProfilesPath, pf)
        s.State.CurrentProfile = req.Profile
        if profile := config.GetCurrentProfile(pf); profile != nil {
            newCfg := config.ConfigFromProfile(s.AtomicConfig.Get(), profile)
            s.AtomicConfig.Set(newCfg)
        }
    }
}
```

If `LoadProfiles` returns an error, the entire block is skipped silently — the user's requested profile is ignored and the tunnel starts with whatever the current config happens to be. No error is reported.

**Fix:** Return an error to the client if profiles can't be loaded:

```go
if err != nil {
    writeError(w, 500, "load profiles: "+err.Error())
    return
}
```

### BUG-4.7 — `handleTunnelStop` succeeds when no tunnel is running (lines 382–389)

```go
if s.TunnelStopFn != nil {
    if err := s.TunnelStopFn(); err != nil {
        writeJSON(w, 500, TunnelResponse{OK: false, Error: err.Error()})
        return
    }
}
writeJSON(w, 200, TunnelResponse{OK: true, Message: "tunnel stopping"})
```

If `TunnelStopFn` is nil (not wired up), the handler returns `{"ok":true,"message":"tunnel stopping"}` as if a tunnel was successfully stopped. The WebUI cannot distinguish "stop was called with no daemon" from "tunnel was running and successfully stopped."

**Fix:** Return an error if `TunnelStopFn` is nil:

```go
if s.TunnelStopFn == nil {
    writeError(w, 503, "tunnel control not available")
    return
}
```

### BUG-4.8 — `handleLatency` accepts invalid port values (line 412)

```go
if p, err := strconv.Atoi(portStr); err == nil {
    port = p
}
```

`strconv.Atoi` accepts negative numbers and zero without error. `net.JoinHostPort("host", "-1")` will produce `"host:-1"` which `net.DialTimeout` will reject — but the error message will be confusing.

**Fix:** Validate port range:

```go
if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p < 65536 {
    port = p
}
```

### BUG-4.9 — Critical data-flow gap: `ConfigFromProfile` drops payload options (handlers.go lines 269, 355 → config.go lines 173–206)

`config.ConfigFromProfile` merges a profile into a config but only copies these profile fields:

- `SSHHost`, `SSHPort`, `SSHUser`, `SSHPassword`, `SSHMode` (overrides)
- `SSHSNIHost`, `HTTPProxyHost`, `HTTPProxyPort` (transport)
- `PayloadEnabled`, `Payload` (payload)

It does **NOT** copy these payload-related profile fields (because `config.Config` doesn't have them):

| Profile field           | Copied to Config? |
|-------------------------|-------------------|
| `PayloadInjectionType`  | **NO** — not in Config |
| `PayloadMethod`         | **NO** — not in Config |
| `PayloadFrontQuery`     | **NO** — not in Config |
| `PayloadBackQuery`      | **NO** — not in Config |
| `PayloadDualConnect`    | **NO** — not in Config |
| `PayloadSplit`          | **NO** — not in Config |

When `handleProfiles` PUT or `handleTunnelStart` calls `ConfigFromProfile` and `AtomicConfig.Set`, these 6 fields are **lost**. The daemon's `TunnelStartFn` receives the merged `Config` but it lacks critical injection parameters.

**Impact:** If the daemon relies solely on `AtomicConfig.Get()` to construct `ConnectConfig.PayloadOpts`, payload injection will be misconfigured (InjectionType = "", Method = "", Split = false, etc.).

**Fix:** Either:
1. Add the 6 fields to `config.Config` and copy them in `ConfigFromProfile`, OR
2. Have the daemon read `profiles.json` directly when constructing `ConnectConfig` (bypassing `AtomicConfig` for payload options).

### BUG-4.10 — Import guards are a code smell (lines 440–441)

```go
var _ = transport.WritePayload
var _ = log.Printf
```

The `transport` and `log` packages are imported but not used in handler code. This is fragile — if either line is accidentally removed, the build breaks. The `transport` package is not needed by the handlers at all; the import exists only because `transport.WritePayload` is referenced in the blank identifier.

**Fix:** Remove both imports and the blank identifier lines. If `transport` types are needed for the `TunnelStartFn` signature, consider a type alias or a thinner interface.

---

## Summary of Findings

| # | Severity | File | Line(s) | Issue |
|---|----------|------|---------|-------|
| 1.1 | HIGH | dialer.go | 27 | Direct dial ignores `Timeout` field |
| 1.2 | HIGH | dialer.go | 70 | Slice bounds panic if proxy response < 16 bytes |
| 1.3 | MEDIUM | dialer.go | 93-105 | Dead code: `resolveHost` never called |
| 1.4 | LOW | dialer.go | 74-87 | Inefficient byte-by-byte header drain |
| 1.5 | MEDIUM | dialer.go | 15-105 | `Dialer` type appears unused (dead code) |
| 2.1 | **CRITICAL** | payload.go | 175-181 | Front/Back inject functions are no-ops |
| 2.2 | HIGH | payload.go | 116-119 | Protocol tautology — always `HTTP/1.0` |
| 2.3 | HIGH | payload.go | 34,36,44 | Dead fields: `FrontQuery`, `BackQuery`, `ExtraHeaders` |
| 2.4 | LOW | payload.go | 105-108 | Dead code: `writePayloadStream` |
| 2.5 | LOW | payload.go | 209-215 | Dead code: `ApplyPayloadToConn` |
| 2.6 | **CRITICAL** | payload.go | 165-167 | Broken CRLF termination logic |
| 2.7 | MEDIUM | payload.go | 139,143 | `[netData]` == `[realData]` when method is CONNECT |
| 2.8 | LOW | payload.go | 57-62 | Undocumented template variables `[lfcr]`,`[cr]`,`[lf]` |
| 3.1 | HIGH | contracts.go | 19-28 | `ConfigResponse` missing 5 fields |
| 4.1 | LOW | handlers.go | 111-113 | Dead if-block — always false |
| 4.2 | HIGH | handlers.go | 129-138 | Config GET missing 5 fields |
| 4.3 | HIGH | handlers.go | 149-172 | Config PUT silently drops 5 fields |
| 4.4 | HIGH | handlers.go | 343 | Unhandled JSON decode error |
| 4.5 | HIGH | handlers.go | 351 | `SaveProfiles` error silently ignored |
| 4.6 | HIGH | handlers.go | 348-357 | Profile load error silently ignored |
| 4.7 | MEDIUM | handlers.go | 382-389 | Stop returns success when `TunnelStopFn` is nil |
| 4.8 | LOW | handlers.go | 412 | No port range validation in latency handler |
| 4.9 | **CRITICAL** | handlers.go + config.go | 269,355 → 173-206 | `ConfigFromProfile` drops 6 payload config fields |
| 4.10 | LOW | handlers.go | 440-441 | Import guards for `transport` and `log` |

## JSON Tag Mismatch / Missing Field Summary

- **`ConfigResponse`** is missing 5 fields present in `config.Config`: `ssh_sni_host`, `http_proxy_host`, `http_proxy_port`, `payload_enabled`, `payload`.
- **`config.Config`** is missing 6 fields present in `config.Profile`: `payload_injection_type`, `payload_method`, `payload_front_query`, `payload_back_query`, `payload_dual_connect`, `payload_split`.
- All `ProfileItem` ↔ `Profile` tags match exactly.
- `StatusResponse`, `TunnelRequest`, `TunnelResponse`, `LatencyResponse`, `ErrorResponse` — no mismatches.

## Nil Dereference / Error Handling Gaps

- `dialer.go:70` — possible slice-bounds panic (`status[:16]` when `len(status)` is 12–15).
- `handlers.go:343` — `json.NewDecoder().Decode()` error not checked.
- `handlers.go:349` — `LoadProfiles` error not handled (silent failure).
- `handlers.go:351` — `SaveProfiles` error not handled (data loss risk).
- `handlers.go:361` — `TunnelStartFn` may be nil; if it is, the handler returns success without starting anything.
- `handlers.go:383` — `TunnelStopFn` may be nil; handler returns success.

## 3 Most Critical Fixes Needed

1. **`config.Config` must include payload injection fields** (`PayloadInjectionType`, `PayloadMethod`, `PayloadFrontQuery`, `PayloadBackQuery`, `PayloadDualConnect`, `PayloadSplit`) and `ConfigFromProfile` must copy them. Without this, profile-based payload injection cannot work.

2. **`applyFrontInject` / `applyBackInject` must be implemented.** Currently they are no-ops, meaning `"front"` and `"back"` injection modes do nothing.

3. **CRLF termination in `buildPayload` must be fixed.** Payloads ending in a single `\r\n` remain unterminated, producing malformed HTTP.
