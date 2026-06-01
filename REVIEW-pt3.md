# Part 3: Line-by-Line Code Review — JSON Contract Mismatches, Bugs, and Logic Errors

## 🔴 CRITICAL — JSON Contract Mismatches (Data Corruption)

### C1. ProfilesFile JSON keys COMPLETELY WRONG — Go vs WebUI

| Layer   | Key for selected profile | Key for profile list |
|---------|--------------------------|----------------------|
| Go `ProfilesFile` (config.go:58-61) | `current` | `items` |
| Go `ProfilesResponse` (contracts.go:52-55) | `current` | `items` |
| **WebUI JS sends** (index.html:958-961) | **`selected_id`** | **`profiles`** |
| **Sample profiles.json** (module/config/profiles.json:2-3) | **`selected_id`** | **`profiles`** |

**Impact:** The Go API will **never correctly parse** profiles from the WebUI. On GET, Go returns `{"current":"...","items":[...]}` — the WebUI reads `data.selected_id` (undefined) and `data.profiles` (undefined), so profiles always appear as an empty list. On PUT, the WebUI sends `{"selected_id":"...","profiles":[...]}` — Go's `ProfilesResponse` unmarshals this with `Current` and `Items` both staying at their zero values (empty string, nil slice). **Profiles are completely broken.**

**Fix:** Either rename Go fields to `selected_id`/`profiles` OR rename WebUI JS fields to `current`/`items`. Also update `ProfilesResponse` to match whichever convention is chosen.

---

### C2. Profile struct has NO `id` field — WebUI uses `id` everywhere

The Go `Profile` struct (config.go:36-55) has these fields: `Name`, `SSHHost`, `SSHPort`, ..., `PayloadSplit`. **There is no `ID` field.**

The WebUI JS uses `p.id` extensively:
- Line 859: `const sel = p.id === selectedProfileId;`
- Line 869: `esc(p.id)`
- Line 871-872: `onclick="openEditor('${esc(p.id)}')"`, `deleteProfileById('${esc(p.id)}')`
- Line 880: `const all = { selected_id: id, profiles };`
- Line 892: `const p = id ? profiles.find(x => x.id === id) || {} : {};`
- Lines 927, 954: Sets `id: editingId` or generates `'pf_' + Date.now()`

When the Go API returns profiles (GET /api/v1/profiles), the JSON objects have **no `id` field** because Go's `Profile` struct doesn't have one. The WebUI receives objects without `id`, so:
- `p.id` is always `undefined`
- `selectedProfileId` never matches anything
- The "Use" button never shows
- Editing relies on array index (broken when reordering/deleting)
- Generated IDs (`pf_` + timestamp) get silently dropped by Go on unmarshal

**Fix:** Add `ID string \`json:"id,omitempty"\`` to the `Profile` struct.

---

### C3. StatusResponse missing `running` and `reconnecting` — WebUI state machine broken

WebUI JS (index.html:722-723):
```js
const running = !!(st.running);
const reconnecting = !!(st.reconnecting);
```

The Go `StatusResponse` (contracts.go:5-16) has **no `running` or `reconnecting` fields**. These are always `undefined` → falsy. The WebUI falls through to "Disconnected" state and **never shows "Connecting" or "Reconnecting" status**, even while the tunnel is starting or retrying.

**Fix:** Add `Running bool \`json:"running"\`` and `Reconnecting bool \`json:"reconnecting"\`` to `StatusResponse`. Set them in `handleStatus`.

---

### C4. StatusResponse missing port/routing fields — Settings page blank

WebUI reads these from status (index.html:784-790):
```js
if (st.api_port) ...
if (st.socks_port) ...
if (st.tproxy_port) ...
if (st.dns_port) ...
if (st.routing_mode) ...
```

Go `StatusResponse` has **none of these**. The settings page will never display actual daemon port values.

**Fix:** Either add these fields to `StatusResponse` (populated from config) OR create a dedicated `/api/v1/settings` endpoint.

---

### C5. LatencyResponse missing `target` field

WebUI (index.html:831):
```js
$('latSub').textContent = 'TCP to ' + (res.target || '8.8.8.8') + ' via tunnel';
```

Go `LatencyResponse` (contracts.go:70-75) has `Host` and `Port` but no `Target` field.

**Fix:** Either rename Go field to `Target` OR change WebUI to read `res.host`.

---

### C6. StatusResponse missing `memory_rss_bytes` fallback

WebUI (index.html:769):
```js
$('memVal').textContent = fmtMb(st.mem_mb ? st.mem_mb * 1048576 : st.memory_rss_bytes);
```

Go has `mem_mb` which is correct, so the fallback is unused. However, note that `mem_mb` is a float64 in MB. The WebUI multiplies by 1048576 to get bytes, then `fmtMb` divides by 1048576. **This double-conversion works correctly** (cancel out) but is unnecessary work. Not a bug, just a code smell.

---

## 🔴 HIGH — Logic Bugs

### H1. DNSX: nil IP from IPv6 leads to `"<nil>"` hostname dial

`resolveViaPing` (dnsx.go:182):
```go
return []net.IP{ip.To4()}, nil
```

If ping resolves to an IPv6 address, `ip.To4()` returns `nil`. The returned slice has length 1 containing a nil IP. Back in `DialContext` (line 69):
```go
target := net.JoinHostPort(ip.String(), port)
```
`nil.String()` returns `"<nil>"`. This passes `"<nil>:22"` to `DialContext`, which tries to resolve `"<nil>"` as a hostname. This will fail with a DNS error but **wastes time and returns a misleading error** instead of "no IPv4 address".

**Fix:**
```go
ip4 := ip.To4()
if ip4 == nil {
    return nil, fmt.Errorf("dnsx: ping resolved to IPv6 address %s, only IPv4 supported", ip)
}
return []net.IP{ip4}, nil
```

---

### H2. handleTunnelStart discards SaveProfiles error

handlers.go:347-358:
```go
pf.Current = req.Profile
config.SaveProfiles(s.ProfilesPath, pf)   // ← ERROR DISCARDED
s.State.CurrentProfile = req.Profile
```

If `SaveProfiles` fails (disk full, permission denied), the new profile is applied to the in-memory atomic config but **NOT persisted**. After a daemon restart, it reverts to the old profile. No error is reported to the user.

**Fix:** Check the error:
```go
if err := config.SaveProfiles(s.ProfilesPath, pf); err != nil {
    writeError(w, 500, "save profile: "+err.Error())
    return
}
```

---

### H3. DNSX Lookup: TOCTOU cache race (waste, not corruption)

dnsx.go:88-106:
```go
r.mu.Lock()
if e, ok := r.cache[host]; ok && time.Now().Before(e.expires) { ... }
r.mu.Unlock()
// ← gap: another goroutine might be resolving the same host
ips, err := resolveHost(ctx, host)
// ...
r.mu.Lock()
r.cache[host] = cacheEntry{...}
r.mu.Unlock()
```

If two goroutines call `Lookup` for the same uncached host simultaneously, both execute the full `resolveHost` → ping → Go DNS chain. The second write just overwrites with identical data. **Not incorrect, just inefficient.**

**Fix:** Use a per-host mutex or a "inflight" map to deduplicate concurrent resolutions.

---

### H4. DNSX DialContext: incorrect error when no addresses reachable

dnsx.go:77-79:
```go
if lastErr == nil {
    lastErr = fmt.Errorf("no reachable addresses for %s", host)
}
```

If `Lookup` returns an empty slice (shouldn't happen because `Lookup` returns error for empty), `lastErr` stays nil. This fallback error is reasonable. However, if `Lookup` returns an error, `DialContext` returns that error (line 63). The `for ips` loop never executes, so `lastErr` stays nil, and the error message from line 63 is returned instead. This is correct.

---

## 🟡 MEDIUM — Logic / Behavior Issues

### M1. Transport: `applyFrontInject` and `applyBackInject` are NO-OPs

payload.go:175-182:
```go
func applyFrontInject(payload string, target Target, opts PayloadOpts) string {
    return payload  // does nothing
}
func applyBackInject(payload string, target Target, opts PayloadOpts) string {
    return payload  // does nothing
}
```

Despite the selectable injection types in the WebUI ("Front" and "Back"), these functions do **absolutely nothing** — they return the payload unchanged. The only injection types that actually modify behavior are `front_query` and `back_query` (which do string replacement on the CONNECT target).

**Impact:** Users selecting "Front" or "Back" injection will see no difference from "Normal" mode. This is misleading.

**Fix:** Either implement the injection logic or remove these options from the WebUI dropdown.

---

### M2. WebUI `PayloadEnabled` derived from textarea, not checkbox

WebUI (index.html:938):
```js
payload_enabled: !!$('efPayload').value.trim(),
```

There is no "Payload Enabled" checkbox in the editor. The `payload_enabled` field is **implicitly derived** from whether the payload textarea has content. If the user has payload text but wanted it disabled, they must clear the text — losing their template. This is a UX issue but the Go side handles it correctly (reads the boolean).

---

### M3. metrics: Used memory excludes SReclaimable

metrics.go:154:
```go
usedKB = memTotal - memFree - buffers - cached
```

Linux kernel slab reclaimable memory (`SReclaimable`) is not subtracted. This slightly overestimates used memory. Standard formula for "used" is:
```
used = MemTotal - MemFree - Buffers - Cached - SReclaimable
```

Not critical but technically inaccurate.

**Fix:** Add `SReclaimable` to the /proc/meminfo parser and subtract it.

---

### M4. metrics: Page size hardcoded to 4096

metrics.go:57:
```go
samp.RSSMB = float64(rssPages*4096) / (1024 * 1024)
```

While 4096 is the standard page size on Android/ARM64, some devices use 16KB pages. A more robust approach would be `sysconf(_SC_PAGESIZE)` or reading `/proc/self/smaps`. Not critical for Android.

---

### M5. webui: Path traversal check uses `strings.HasPrefix` — case-insensitive bypass on case-insensitive FS?

webui.go:55:
```go
if !strings.HasPrefix(diskPath, h.Webroot) {
```

On Linux (ext4/f2fs), this is fine. On case-insensitive filesystems (unlikely on Android but theoretically possible if someone mounts vfat), an attacker could bypass with case tricks. Not exploitable on Android's default filesystems.

---

### M6. config: `sample config.json` has no transport fields

module/config/config.json:
```json
{
    "ssh_host": "",
    "ssh_port": 22,
    "ssh_user": "",
    "ssh_password": "",
    "ssh_mode": "direct",
    "socks_port": 1080,
    "api_port": 9190,
    "work_dir": "/data/adb/sshcustom"
}
```

The `Config` struct (config.go:14-32) has additional fields (`ssh_sni_host`, `http_proxy_host`, `http_proxy_port`, `payload_enabled`, `payload`) that are NOT in the sample config. These have `omitempty` so they won't appear when serialized if empty, but it's worth documenting that they exist and are populated from the profile, not from config.json.

---

## 🟢 LOW — Minor Issues & Code Quality

### L1. metrics.go line 42 comment says `/proc/stat` but means `/proc/self/stat`

```go
// /proc/stat ticks are in USER_HZ (usually 100), convert to seconds
```

Should say `/proc/self/stat ticks`. `/proc/stat` is system-wide, but the code reads process-specific ticks from `/proc/self/stat`.

---

### L2. webui.go line 93: dead code assertion

```go
var _ = fs.StatFS(nil)
```

This line ensures the `io/fs` import is used (to satisfy Go's unused import check). It works but is an unusual pattern. The conventional approach is to reference `fs.StatFS` in an actual type assertion or interface check. Not a bug.

---

### L3. version.go: dev suffix in version string

```go
var Version = "3.1.0-dev"
```

Make sure the build pipeline overrides this with `-ldflags`. Currently, if built without flags, it reports `3.1.0-dev` which is fine for development.

---

### L4. go.mod: `golang.org/x/sys` marked indirect but may be direct

```go
require golang.org/x/sys v0.28.0 // indirect
```

If the codebase uses `golang.org/x/term` or similar subsystems that require `x/sys`, this should be indirect. If any `.go` file imports `golang.org/x/sys` directly, the `// indirect` comment is wrong. Verify by running `go mod tidy`.

---

## 📊 Summary: JSON Field Match Table

| Field | Go Config | Go Profile | Go StatusResponse | Go LatencyResponse | Go ProfilesResponse | WebUI sends | WebUI expects |
|-------|-----------|------------|-------------------|---------------------|-----------------------|-------------|---------------|
| `ssh_host` | ✅ | ✅ | ✅ | — | — | ✅ | ✅ |
| `ssh_port` | ✅ | ✅ | — | — | — | ✅ | — |
| `ssh_user` | ✅ | ✅ | — | — | — | ✅ | — |
| `ssh_password` | ✅ | ✅ | — | — | — | ✅ | — |
| `ssh_mode` | ✅ | ✅ | ✅ | — | — | ✅ | ✅ |
| `ssh_sni_host` | ✅ | ✅ | — | — | — | ✅ | — |
| `http_proxy_host` | ✅ | ✅ | — | — | — | ✅ | — |
| `http_proxy_port` | ✅ | ✅ | — | — | — | ✅ | — |
| `payload` | ✅ | ✅ | — | — | — | ✅ | — |
| `payload_enabled` | ✅ | ✅ | — | — | — | ✅ | — |
| `payload_injection_type` | — | ✅ | — | — | — | ✅ | — |
| `payload_method` | — | ✅ | — | — | — | ✅ | — |
| `payload_front_query` | — | ✅ | — | — | — | ✅ | — |
| `payload_back_query` | — | ✅ | — | — | — | ✅ | — |
| `payload_dual_connect` | — | ✅ | — | — | — | ✅ | — |
| `payload_split` | — | ✅ | — | — | — | ✅ | — |
| `socks_port` | ✅ | — | — | — | — | — | ✅ |
| `api_port` | ✅ | — | — | — | — | — | ✅ |
| `work_dir` | ✅ | — | — | — | — | — | — |
| `name` | — | ✅ | — | — | — | ✅ | — |
| **`id`** | — | **❌ MISSING** | — | — | — | ✅ | ✅ |
| **`selected_id`** | — | — | — | — | **❌ `current`** | ✅ | ✅ |
| **`profiles`** | — | — | — | — | **❌ `items`** | ✅ | ✅ |
| `connected` | — | — | ✅ | — | — | — | ✅ |
| **`running`** | — | — | **❌ MISSING** | — | — | — | ✅ |
| **`reconnecting`** | — | — | **❌ MISSING** | — | — | — | ✅ |
| `profile` | — | — | ✅ | — | — | — | ✅ |
| `mem_mb` | — | — | ✅ | — | — | — | ✅ |
| `cpu_pct` | — | — | ✅ | — | — | — | ✅ |
| `uptime_seconds` | — | — | ✅ | — | — | — | ✅ |
| `active_connections` | — | — | ✅ | — | — | — | ✅ |
| `version` | — | — | ✅ | — | — | — | ✅ |
| `last_error` | — | — | ✅ | — | — | — | ✅ |
| **`tproxy_port`** | — | — | **❌ MISSING** | — | — | — | ✅ |
| **`dns_port`** | — | — | **❌ MISSING** | — | — | — | ✅ |
| **`routing_mode`** | — | — | **❌ MISSING** | — | — | — | ✅ |
| `latency_ms` | — | — | — | ✅ | — | — | ✅ |
| **`target`** | — | — | — | **❌ `host`** | — | — | ✅ |
| `host` | — | — | — | ✅ | — | — | — |
| `port` | — | — | — | ✅ | — | — | — |
| `error` | — | — | — | ✅ | — | — | — |

**❌ = mismatch — API contract is broken for this field**

---

## 🔧 Recommended Fix Priority

1. **IMMEDIATE:** Rename `ProfilesFile` JSON keys from `current`/`items` to `selected_id`/`profiles` (or vice versa in WebUI)
2. **IMMEDIATE:** Add `ID string \`json:"id,omitempty"\`` to `Profile` struct
3. **HIGH:** Add `running`, `reconnecting`, `tproxy_port`, `dns_port`, `routing_mode` to `StatusResponse`
4. **HIGH:** Add `target` field to `LatencyResponse` or fix WebUI to use `host`
5. **HIGH:** Fix nil-IP-in-slice bug in `resolveViaPing`
6. **HIGH:** Check `SaveProfiles` error in `handleTunnelStart`
7. **MEDIUM:** Implement `applyFrontInject`/`applyBackInject` or remove from UI
8. **LOW:** Fix comments, add `SReclaimable` to meminfo parser, cosmetic fixes
