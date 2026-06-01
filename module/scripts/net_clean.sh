#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Emergency Crash Recovery
# Kills all processes and purges ALL iptables rules + policy routing.
# Safe to run multiple times (fully idempotent).
#=============================================================================

BOX_DIR="/data/adb/sshcustom"
RUN_DIR="${BOX_DIR}/run"
CLEAN_LOG="${RUN_DIR}/net_clean.log"

mkdir -p "$RUN_DIR"

log() {
	echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$CLEAN_LOG"
}

run() {
	"$@" >/dev/null 2>&1 || true
}

log "========== emergency cleanup start =========="

# ---- 1. Kill all SSHCustom processes ------------------------------------
log "killing all sshcustomd processes"
run killall sshcustomd
for _i in 1 2 3; do
	_pids=""
	_pids="$(busybox pidof sshcustomd 2>/dev/null)" || true
	[ -z "$_pids" ] && _pids="$(busybox pidof /data/adb/sshcustom/bin/sshcustomd 2>/dev/null)" || true
	[ -z "$_pids" ] && break
	for _p in $_pids; do
		run kill -KILL "$_p"
	done
	sleep 1
done

log "killing all hev-socks5-tproxy processes"
run killall hev-socks5-tproxy
for _i in 1 2 3; do
	_pids=""
	_pids="$(busybox pidof hev-socks5-tproxy 2>/dev/null)" || true
	[ -z "$_pids" ] && _pids="$(busybox pidof /data/adb/sshcustom/bin/hev-socks5-tproxy 2>/dev/null)" || true
	[ -z "$_pids" ] && break
	for _p in $_pids; do
		run kill -KILL "$_p"
	done
	sleep 1
done

# Clean PID files
rm -f "${RUN_DIR}/daemon.pid" "${RUN_DIR}/hev.pid" "${RUN_DIR}/watchdog.pid" \
      "${RUN_DIR}/enabled" "${RUN_DIR}/network_paused"

# ---- 2. Remove ALL iptables chains (current + legacy) -------------------
log "purging iptables chains"

# Helper: remove jump + flush + delete a chain from a table
purge_chain() {
	_table="$1"
	_chain="$2"
	_bin="${3:-iptables}"
	run $_bin -t "$_table" -D PREROUTING -j "$_chain"
	run $_bin -t "$_table" -D OUTPUT -j "$_chain"
	run $_bin -t "$_table" -D INPUT -j "$_chain"
	run $_bin -t "$_table" -D FORWARD -j "$_chain"
	run $_bin -t "$_table" -F "$_chain"
	run $_bin -t "$_table" -X "$_chain"
}

# v3.1.0 chain names
CHAINS_V3="
SSHC_TPROXY_PRE
SSHC_TPROXY_OUT
SSHC_REDIR_PRE
SSHC_REDIR_OUT
"

# Legacy chain names (v2.x and earlier)
CHAINS_LEGACY="
SSHC_OUTPUT
SSHC_PREROUTING
SSHC_PROXY
SSHC_DNS
SSHC_HOTSPOT
SSHC_HOTSPOT_DNS
"

ALL_CHAINS="$CHAINS_V3 $CHAINS_LEGACY"

# IPv4 purge: mangle + nat tables
for _c in $ALL_CHAINS; do
	[ -z "$_c" ] && continue
	purge_chain "mangle" "$_c" iptables
	purge_chain "nat"    "$_c" iptables
	purge_chain "filter" "$_c" iptables
done

# IPv6 purge: mangle + nat tables
for _c in $ALL_CHAINS; do
	[ -z "$_c" ] && continue
	purge_chain "mangle" "$_c" ip6tables
	purge_chain "nat"    "$_c" ip6tables
	purge_chain "filter" "$_c" ip6tables
done

# Extra: clean any DROP rules for QUIC (UDP 443/80) the daemon may have added
for _p in 443 80; do
	_i=0
	while [ "$_i" -lt 5 ]; do
		run iptables -t filter -D OUTPUT -p udp --dport "$_p" -j DROP || break
		_i=$((_i + 1))
	done
done

# Also clean hotspot FORWARD ACCEPT (v2.x)
run iptables -D FORWARD -j ACCEPT
run ip6tables -D FORWARD -j ACCEPT

# ---- 3. Clean policy routing --------------------------------------------
log "cleaning policy routing"
_ip=""
command -v ip >/dev/null 2>&1 && _ip="ip"
[ -z "$_ip" ] && command -v busybox >/dev/null 2>&1 && _ip="busybox ip"

if [ -n "$_ip" ]; then
	# Remove fwmark rule (v3.1.0 mark 0x1 table 100)
	run $_ip rule del fwmark 0x1 table 100
	run $_ip rule del fwmark 0x1 lookup 100

	# Remove various legacy marks (20, 25, 64)
	for _mark in 20 25 64; do
		run $_ip rule del fwmark $_mark table $_mark
		run $_ip rule del fwmark $_mark table 100
	done

	# Remove local routes
	run $_ip route del local 0.0.0.0/0 dev lo table 100
	for _tbl in 20 25 64; do
		run $_ip route del local 0.0.0.0/0 dev lo table $_tbl
	done

	# IPv6
	if command -v ip6tables >/dev/null 2>&1; then
		run $_ip -6 rule del fwmark 0x1 table 100
		run $_ip -6 route del local ::/0 dev lo table 100
		for _mark in 20 25 64; do
			run $_ip -6 rule del fwmark $_mark table $_mark
			run $_ip -6 route del local ::/0 dev lo table $_mark
		done
	fi
fi

# ---- 4. Restore sysctls -------------------------------------------------
log "restoring sysctls"

# Restore IPv6 (if daemon disabled it)
run sysctl -w net.ipv6.conf.all.disable_ipv6=0
run sysctl -w net.ipv6.conf.default.disable_ipv6=0

# Restore TCP tuning to safe defaults
run sysctl -w net.ipv4.tcp_congestion_control=cubic
run sysctl -w net.ipv4.tcp_fastopen=1

# Restore captive portal settings
run settings put global captive_portal_mode 1
run settings put global captive_portal_detection_enabled 1
run settings put global captive_portal_use_https 1
run settings delete global captive_portal_server
run settings delete global captive_portal_http_url
run settings delete global captive_portal_https_url

log "========== emergency cleanup complete =========="
exit 0
