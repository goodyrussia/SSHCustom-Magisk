# SSHCustom-Magisk v3.1.0 — Line-by-Line Shell Audit

**Date**: 2026-06-01
**Scope**: All 7 shell scripts under `module/`
**Methodology**: Manual line-by-line review against build.sh, module.prop, config.json, tproxy.yaml, and actual filesystem layout

---

## 🔴 CRITICAL BUGS (will prevent installation or runtime operation)

### 1. `customize.sh` — ARCH-to-directory mismatch (LINES 37, 41, 61)

**File**: `module/customize.sh`
**Severity**: 🔴 CRITICAL — installation will abort

```sh
# Line 37:  arm64-v8a → ARCH="arm64"
# Line 41:  armeabi-v7a → ARCH="arm"
# Line 61:  SRC_BIN_DIR="${MODDIR}/bin/${ARCH}"
#           → resolves to bin/arm64 or bin/arm
```

**Actual directories produced by build.sh**:
```
module/bin/arm64-v8a/sshcustomd
module/bin/arm64-v8a/hev-socks5-tproxy
module/bin/armeabi-v7a/sshcustomd
module/bin/armeabi-v7a/hev-socks5-tproxy
```

`SRC_BIN_DIR` resolves to `bin/arm64` or `bin/arm` — **neither exists**. Line 62–65 will trigger:
```
ERROR: Binary source directory not found: .../bin/arm64
abort "Missing binaries for arm64"
```

**Fix**: Either rename directories to `bin/arm64` / `bin/arm`, or change the ARCH mapping:
```sh
arm64-v8a)    ARCH="arm64-v8a" ;;
armeabi-v7a)  ARCH="armeabi-v7a" ;;
```

---

### 2. `sshcustom.iptables` — `multiport --dports` uses space-separated ports (LINES 13, 143–144)

**File**: `module/scripts/sshcustom.iptables`
**Severity**: 🔴 CRITICAL — bypass rules silently fail, traffic loops may occur

```sh
# Line 13
SELF_PORTS="${SELF_PORTS:-9190 1080 1088 1053}"
# This is SPACE-separated.

# Lines 143-144 (in _bypass_rules, called for every chain)
iptables -t mangle -A "$_chain" -p tcp -m multiport --dports "$_sp" -j ACCEPT
iptables -t nat    -A "$_chain" -p tcp -m multiport --dports "$_sp" -j ACCEPT
```

The `multiport` module's `--dports` requires **comma-separated** ports. Passing `"9190 1080 1088 1053"` causes iptables to fail. The `2>/dev/null || true` on line 144 **silently swallows the error** on the nat side, but the mangle rule on line 143 has **no error suppression** — it will print an error to stderr and the rule won't be added.

**Impact**: Self-port bypass never works. Traffic to ports 9190, 1080, 1088, 1053 enters the proxy, creating potential routing loops.

**Fix**: Change to comma-separated:
```sh
SELF_PORTS="${SELF_PORTS:-9190,1080,1088,1053}"
# And update line 141: _sp should use comma format throughout
```

---

## 🟠 HIGH-SEVERITY BUGS (runtime failures or security issues)

### 3. `customize.sh` — `config.json` world-readable despite containing `ssh_password` (LINE 142)

**File**: `module/customize.sh`
**Severity**: 🟠 HIGH — credential exposure

```sh
# Line 142
chmod 0644 "${CONFIG_DST}/config.json" 2>/dev/null || true
```

`config.json` contains `"ssh_password": ""`. Any app with storage permission can read it. Meanwhile, `profiles.json` (line 143) is correctly set to `0600`.

**Fix**: Change to `chmod 0600`.

---

### 4. `sshcustom.iptables` — TPROXY PREROUTING DNS rule BEFORE mark-bypass creates re-injection loop risk (LINE 184)

**File**: `module/scripts/sshcustom.iptables`
**Severity**: 🟠 HIGH — DNS lookup loops possible

```sh
# Line 184 — placed BEFORE bypass rules (line 187)
iptables -t mangle -A "$C_TPROXY_PREROUTING" -p udp --dport 53 \
  -j TPROXY --on-port "$DNS_PORT" --tproxy-mark "$FW_MARK"
```

The comment on lines 182-183 says this ordering is intentional: "before mark bypass so re-injected local DNS with fwmark still gets caught." **However**, the bypass rules on lines 131-132 check for the mark:

```sh
iptables -t mangle -A "$_chain" -m mark --mark "$FW_MARK/$FW_MASK" -j ACCEPT
```

These bypass rules are added AFTER the DNS TPROXY rule. But because of how iptables chain traversal works (first-match wins), the DNS TPROXY rule at position 1 fires first. When the DNS listener re-injects the packet, it comes back through PREROUTING with the fwmark set. This time, the mark bypass at position 2+ would match and ACCEPT, preventing double-proxy. **This is correct as designed**, but extremely fragile — if the chain order changes in a future edit, DNS will loop.

**Recommendation**: Add a comment block warning and consider adding an explicit test.

---

### 5. `sshcustom.iptables` — `_bypass_rules()` adds to BOTH mangle AND nat tables regardless of mode (LINES 131-161)

**File**: `module/scripts/sshcustom.iptables`
**Severity**: 🟠 HIGH — error suppression masks permanent misconfiguration

In TPROXY mode, the custom chains (`SSHC_TPROXY_PRE`, `SSHC_TPROXY_OUT`) exist **only in the mangle table**. But `_bypass_rules()` unconditionally adds rules to **both mangle and nat**:

```sh
iptables -t mangle -A "$_chain" ...   # succeeds in TPROXY mode
iptables -t nat    -A "$_chain" ...   # FAILS — chain doesn't exist in nat
```

The `2>/dev/null || true` on each nat rule suppresses the error. Same issue in reverse for REDIRECT mode (chains exist in nat, not mangle).

**Impact**: Every `_bypass_rules` call generates 8+ silent iptables errors. If `iptables -w` lock contention occurs, these could time out. More seriously, if someone adds `set -e` to the script, it would abort.

**Fix**: Pass a table parameter or check mode before adding:
```sh
_bypass_rules() {
    _chain="$1"; _typ="$2"; _table="$3"  # "mangle" or "nat"
    ...
    iptables -t "$_table" -A "$_chain" ...
}
```

---

### 6. `sshcustom.sh` — `start_hev()` uses `nohup ... &` but `$!` may capture wrong PID (LINE 136-137)

**File**: `module/scripts/sshcustom.sh`
**Severity**: 🟠 HIGH — PID tracking unreliable

```sh
nohup "$BIN_HEV" >/dev/null 2>&1 &
_pid="$!"
echo "$_pid" > "$PID_HEV"
```

`$!` returns the PID of the **nohup** process, not necessarily the binary itself. On some Android shells, `nohup` may fork and the binary gets a different PID. The `pid_alive()` function uses `kill -0` to check — it may get false negatives if nohup exits but the binary lives, or false positives if the binary dies but nohup stays.

**Fix**: Use `start-stop-daemon` or write the PID file from the binary itself, or use `setsid` instead of `nohup`:
```sh
setsid "$BIN_HEV" >/dev/null 2>&1 < /dev/null &
```

---

### 7. `sshcustom.sh` — `start_hev()` doesn't pass config path to binary (LINE 136)

**File**: `module/scripts/sshcustom.sh`
**Severity**: 🟠 HIGH — binary may not find its YAML config

```sh
# Line 133 — creates a "hev" subdirectory that's never used
mkdir -p "${CONFIG_DIR}/hev" 2>/dev/null

# Line 136 — starts binary with no config flag
nohup "$BIN_HEV" >/dev/null 2>&1 &
```

The actual config is at `${CONFIG_DIR}/tproxy.yaml`. The binary `hev-socks5-tproxy` was compiled with a different project and may expect config at a standard location (e.g., `/etc/hev-socks5-tproxy.yml` or `./tproxy.yaml` relative to CWD). The `mkdir -p ${CONFIG_DIR}/hev` suggests the binary might look for config under `hev/`, but no config is ever copied there.

**Impact**: The TPROXY listener may start with wrong/default ports, or fail to start entirely. The `wait_for_listener` on line 143 may succeed even with wrong ports.

**Fix**: Verify the binary's config discovery mechanism and either pass `-c` flag or copy `tproxy.yaml` to the expected location.

**Note**: The exported `BOX_DIR`, `CONFIG_DIR`, `RUN_DIR` environment variables may be used by the binary for config discovery, but this is unconfirmed without source access.

---

## 🟡 MEDIUM-SEVERITY BUGS (operational issues, edge cases)

### 8. `service.sh` — infinite boot wait with no timeout (LINES 19-23)

**File**: `module/service.sh`
**Severity**: 🟡 MEDIUM — potential hang on custom ROMs

```sh
while true; do
    _bc="$(getprop sys.boot_completed 2>/dev/null)"
    [ "$_bc" = "1" ] && break
    sleep 5
done
```

On some custom ROMs or devices with broken init, `sys.boot_completed` may never be set to "1". This loop will run forever, consuming CPU and preventing other late-start services from running.

**Fix**: Add a timeout (e.g., 5 minutes = 60 iterations):
```sh
_i=0
while [ "$_i" -lt 60 ]; do
    _bc="$(getprop sys.boot_completed 2>/dev/null)"
    [ "$_bc" = "1" ] && break
    sleep 5
    _i=$((_i + 1))
done
```

---

### 9. `sshcustom.sh` — logs written with `$*` lose argument boundaries (LINE 37)

**File**: `module/scripts/sshcustom.sh`
**Severity**: 🟡 MEDIUM — log readability

```sh
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$CORE_LOG"
}
```

`$*` joins all arguments with the first character of `$IFS` (space). If any argument contains spaces or special characters, the log line may be ambiguous. Prefer `"$@"` joined with spaces:
```sh
log() {
    printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$CORE_LOG"
}
```
(This is POSIX — `"$*"` in double quotes uses first IFS char, which is fine. The real issue is using unquoted `$*`. Should be `"$*"`.)

---

### 10. `sshcustom.iptables` — `ssh_host_ip()` called twice per chain (LINE 128)

**File**: `module/scripts/sshcustom.iptables`
**Severity**: 🟡 MEDIUM — unnecessary I/O

```sh
ssh_host_ip >/dev/null 2>&1 && _ssh_ip="$(ssh_host_ip)"
```

Calls `ssh_host_ip` twice — first to check success, then to capture output. Each call reads `${BOX_DIR}/run/ssh_host_ip`. While not a correctness bug, it doubles I/O for a file that could change between reads (race condition).

**Fix**:
```sh
_ssh_ip="$(ssh_host_ip 2>/dev/null)" || _ssh_ip=""
```

---

### 11. `sshcustom.sh` — `wait_for_listener()` blocks for full timeout even when no nc is available (LINES 59-66)

**File**: `module/scripts/sshcustom.sh`
**Severity**: 🟡 MEDIUM — unnecessary delay

When neither `nc` nor `busybox nc` exists, the else branch does `sleep 1; i++; continue`. This loops for the full timeout period (default 10 or 15 seconds) doing nothing but sleeping — it can never succeed without nc. The function should return 1 immediately when no connectivity checker is available.

---

### 12. `net_clean.sh` — IPv6 routing cleanup gated on `ip6tables` not `ip -6` (LINES 144-151)

**File**: `module/scripts/net_clean.sh`
**Severity**: 🟡 MEDIUM — IPv6 routes may be left behind

```sh
if command -v ip6tables >/dev/null 2>&1; then
    run $_ip -6 rule del fwmark 0x1 table 100
    ...
fi
```

The condition checks for `ip6tables` binary, but the operation uses `ip -6`. These are unrelated — `ip6tables` could exist while `ip -6` is unsupported, or vice versa. Should check:
```sh
if $_ip -6 route list table 100 >/dev/null 2>&1; then
```

---

### 13. `customize.sh` — `chmod 0755` on directory arguments may fail silently (LINE 141)

**File**: `module/customize.sh`
**Severity**: 🟡 MEDIUM — directories may have wrong permissions

```sh
chmod 0755 "$BOX_DIR" "$BIN_DST" "$SCRIPTS_DST" "$CONFIG_DST" "$RUN_DST" "$WEBROOT_DST"
```

No error handling. If one directory doesn't exist (e.g., creation failed silently earlier), `chmod` returns non-zero but the script continues. All 6 directories are created with `mkdir -p 2>/dev/null` — the `2>/dev/null` suppresses "already exists" errors, but also suppresses "permission denied" errors.

---

## 🔵 LOW-SEVERITY / COSMETIC

### 14. `sshcustom.sh` — Typo: "FATAL" should be "FATAL" (LINES 273, 277, 281)

```sh
log "FATAL: hev-socks5-tproxy failed"  # should be FATAL
log "FATAL: sshcustomd failed"
log "FATAL: iptables failed"
```

### 15. `sshcustom.sh` — `do_start()` sources iptables script TWICE (LINES 267, 207)

Speed boost sources it at line 267; `enable_iptables()` sources it again at line 207. Harmless (sourcing is idempotent for function definitions) but wasteful.

### 16. `sshcustom.sh` — `speed_boost` called redundantly (LINES 268, 173/227)

Step 1 calls `speed_boost` explicitly. Then `enable_tproxy()` or `enable_redirect()` call it again. Triple/double sysctl writes — idempotent, just slow.

### 17. `sshcustom.iptables` — BYPASS_CIDRS includes RFC 5737 TEST-NET addresses (LINES 33-39)

```
192.0.2.0/24      # TEST-NET-1
198.51.100.0/24   # TEST-NET-2
203.0.113.0/24    # TEST-NET-3
```

These documentation/test networks should never appear in real traffic. Including them adds 12 unnecessary rules per chain (4 tables × 3 CIDRs). Not harmful, just bloat.

### 18. `net_clean.sh` — `iptables -D FORWARD -j ACCEPT` may remove wrong rule (LINE 117)

```sh
run iptables -D FORWARD -j ACCEPT
```

Deletes the **first** rule matching `-j ACCEPT` in the FORWARD chain. If another module or the system has ACCEPT rules in FORWARD, this could remove the wrong one. Should use a more specific match.

### 19. `customize.sh` — no `x86`/`x86_64` support (LINES 44-48)

Aborts for non-ARM architectures. Chromebooks and emulators using x86_64 are unsupported. Not a bug if intentional, but should be documented.

---

## ✅ VERIFIED CORRECT

| Item | File | Status |
|------|------|--------|
| MODDIR uses `cd+dirname+pwd`, no `..` | customize.sh:14 | ✅ CORRECT |
| BOX_DIR = `/data/adb/sshcustom` (all files) | All 7 scripts | ✅ CONSISTENT |
| Binary names: `sshcustomd`, `hev-socks5-tproxy` match build.sh | sshcustom.sh:15-16 | ✅ CORRECT |
| Chain names unique, no collision with system chains | iptables:21-24 | ✅ CORRECT |
| TPROXY: DNS hijack via TPROXY in PREROUTING, MARK in OUTPUT | iptables:184,194 | ✅ CORRECT |
| REDIRECT: DNS + TCP via nat REDIRECT target | iptables:237,243,246,250 | ✅ CORRECT |
| Policy routing: fwmark → table 100 → local dev lo | iptables:208-217 | ✅ CORRECT |
| `disable()` is fully idempotent (all deletes `2>/dev/null \|\| true`) | iptables:261-321 | ✅ CORRECT |
| Stop order: iptables → daemon → hev (reverse of start) | sshcustom.sh:293-322 | ✅ CORRECT |
| Start order: speed_boost → hev → daemon → iptables | sshcustom.sh:255-290 | ✅ CORRECT |
| `kill_tracked()`: TERM → 5s wait → KILL | sshcustom.sh:82-100 | ✅ CORRECT |
| `net_clean.sh`: kills by name + pidof fallback, 3 retries | net_clean.sh:25-49 | ✅ CORRECT |
| Legacy chain cleanup (v2.x) included in disable | iptables:295-304 | ✅ CORRECT |
| Config preserve logic (don't overwrite existing) | customize.sh:95-112 | ✅ CORRECT |
| `post-fs-data.sh` early mkdir | post-fs-data.sh:6 | ✅ CORRECT |
| `uninstall.sh` full cleanup + rm -rf | uninstall.sh:11-38 | ✅ CORRECT |
| `-w 100` xtables lock wait on all iptables calls | iptables:52-57 | ✅ CORRECT |

---

## 🔴 SUMMARY OF REQUIRED FIXES

1. **CRITICAL**: Fix ARCH-to-directory mapping in `customize.sh` (arch → `arm64-v8a`/`armeabi-v7a`)
2. **CRITICAL**: Change `SELF_PORTS` to comma-separated for multiport compatibility
3. **HIGH**: Set `config.json` to `0600` (contains `ssh_password`)
4. **HIGH**: Verify and fix `hev-socks5-tproxy` config discovery (pass `-c` flag or copy `tproxy.yaml`)
5. **HIGH**: Fix `_bypass_rules()` to only add rules to the relevant table (mangle or nat)
6. **HIGH**: Use `setsid` instead of `nohup` for reliable PID capture
7. **MEDIUM**: Add timeout to service.sh boot wait loop
8. **MEDIUM**: Fix `wait_for_listener()` to fail fast when no nc is available

---

## 📊 SEVERITY COUNT

| Severity | Count |
|----------|-------|
| 🔴 CRITICAL | 2 |
| 🟠 HIGH | 5 |
| 🟡 MEDIUM | 6 |
| 🔵 LOW | 6 |
| **Total issues** | **19** |
