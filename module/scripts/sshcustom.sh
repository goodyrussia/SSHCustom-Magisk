#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Orchestrator
# Controls: start | stop | restart | status
#=============================================================================

BOX_DIR="/data/adb/sshcustom"
BIN_DIR="${BOX_DIR}/bin"
SCRIPTS_DIR="${BOX_DIR}/scripts"
CONFIG_DIR="${BOX_DIR}/config"
RUN_DIR="${BOX_DIR}/run"
WEBROOT_DIR="${BOX_DIR}/webroot"

# Binaries
BIN_HEV="${BIN_DIR}/hev-socks5-tproxy"
BIN_DAEMON="${BIN_DIR}/sshcustomd"
CONFIG_FILE="${CONFIG_DIR}/config.json"
PROFILES_FILE="${CONFIG_DIR}/profiles.json"

# Runtime markers
PID_DAEMON="${RUN_DIR}/daemon.pid"
PID_HEV="${RUN_DIR}/hev.pid"
ENABLED_FILE="${RUN_DIR}/enabled"

# Logs
CORE_LOG="${RUN_DIR}/core.log"
IPT_LOG_FILE="${RUN_DIR}/iptables.log"

# Scripts
IPT_SCRIPT="${SCRIPTS_DIR}/sshcustom.iptables"

# API
API_URL="http://127.0.0.1:9190/api/health"

# ---- helpers ------------------------------------------------------------
log() {
	echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$CORE_LOG"
}

pid_alive() {
	_p=""
	[ -n "$1" ] && _p="$1" || return 1
	kill -0 "$_p" 2>/dev/null
}

read_pid() {
	[ -f "$1" ] || return 1
	_p="$(cat "$1" 2>/dev/null)"
	[ -n "$_p" ] && echo "$_p" && return 0
	return 1
}

wait_for_listener() {
	_port="$1"
	_timeout="${2:-10}"
	_i=0
	while [ "$_i" -lt "$_timeout" ]; do
		if command -v nc >/dev/null 2>&1; then
			nc -z 127.0.0.1 "$_port" 2>/dev/null && return 0
		elif command -v busybox >/dev/null 2>&1; then
			busybox nc -z 127.0.0.1 "$_port" 2>/dev/null && return 0
		else
			sleep 1
			_i=$((_i + 1))
			continue
		fi
		sleep 1
		_i=$((_i + 1))
	done
	return 1
}

api_ready() {
	_h=""
	command -v curl >/dev/null 2>&1 && _h="curl -fsS --max-time 2"
	[ -z "$_h" ] && command -v wget >/dev/null 2>&1 && _h="wget -q -T 2 -O /dev/null"
	[ -z "$_h" ] && return 1
	$_h "$API_URL" >/dev/null 2>&1
}

# ---- kill a tracked process gracefully ----------------------------------
kill_tracked() {
	_pidfile="$1"
	_name="$2"
	if [ -f "$_pidfile" ]; then
		_pid="$(cat "$_pidfile" 2>/dev/null)"
		if pid_alive "$_pid"; then
			log "stopping $_name (pid=$_pid)"
			kill -TERM "$_pid" 2>/dev/null || true
			_j=0
			while [ "$_j" -lt 5 ]; do
				pid_alive "$_pid" || break
				sleep 1
				_j=$((_j + 1))
			done
			pid_alive "$_pid" && kill -KILL "$_pid" 2>/dev/null || true
		fi
		rm -f "$_pidfile"
	fi
}

# ---- kill by name (fallback) --------------------------------------------
kill_by_name() {
	_name="$1"
	_exe="$2"
	# Try by process name first
	killall "$_name" 2>/dev/null || true
	sleep 1
	# Also try to find by executable path
	if [ -n "$_exe" ] && [ -f "$_exe" ]; then
		_pids=""
		_pids="$(busybox pidof "$_exe" 2>/dev/null)" || true
		if [ -z "$_pids" ]; then
			_pids="$(ps -A 2>/dev/null | grep "$_exe" | grep -v grep | awk '{print $1}')" || true
		fi
		for _p in $_pids; do
			kill -TERM "$_p" 2>/dev/null || true
		done
	fi
}

# ---- start hev-socks5-tproxy --------------------------------------------
start_hev() {
	log "starting hev-socks5-tproxy"
	[ -x "$BIN_HEV" ] || { log "ERROR: $BIN_HEV not found"; return 1; }

	# Kill any stale instance
	kill_tracked "$PID_HEV" "hev-socks5-tproxy"
	kill_by_name "hev-socks5-tproxy" "$BIN_HEV"
	sleep 1

	# Ensure config directory exists for hev
	mkdir -p "${CONFIG_DIR}/hev" 2>/dev/null
	export BOX_DIR CONFIG_DIR RUN_DIR

	nohup "$BIN_HEV" >/dev/null 2>&1 &
	_pid="$!"
	echo "$_pid" > "$PID_HEV"
	log "hev-socks5-tproxy started (pid=$_pid)"

	# Wait for TPROXY listener
	log "waiting for TPROXY listener on port ${TPROXY_PORT:-1088}..."
	wait_for_listener "${TPROXY_PORT:-1088}" 15 && {
		log "TPROXY listener ready"
		return 0
	}
	log "WARNING: TPROXY listener not detected, continuing anyway"
	return 0
}

# ---- start sshcustomd daemon --------------------------------------------
start_daemon() {
	log "starting sshcustomd daemon"
	[ -x "$BIN_DAEMON" ] || { log "ERROR: $BIN_DAEMON not found"; return 1; }
	[ -f "$CONFIG_FILE" ] || { log "ERROR: config.json missing"; return 1; }
	[ -f "$PROFILES_FILE" ] || { log "ERROR: profiles.json missing"; return 1; }

	# Kill any stale instance
	kill_tracked "$PID_DAEMON" "sshcustomd"
	kill_by_name "sshcustomd" "$BIN_DAEMON"
	sleep 1

	mkdir -p "$RUN_DIR"
	export BOX_DIR CONFIG_DIR RUN_DIR

	nohup "$BIN_DAEMON" run -c "$CONFIG_FILE" -p "$PROFILES_FILE" -w "$BOX_DIR" >/dev/null 2>&1 &
	_pid="$!"
	echo "$_pid" > "$PID_DAEMON"
	log "sshcustomd started (pid=$_pid)"

	# Wait for API
	log "waiting for WebUI API on port 9190..."
	_j=0
	while [ "$_j" -lt 10 ]; do
		if ! pid_alive "$_pid"; then
			log "ERROR: sshcustomd died during startup"
			rm -f "$PID_DAEMON"
			return 1
		fi
		api_ready && { log "WebUI API ready"; return 0; }
		sleep 1
		_j=$((_j + 1))
	done

	if pid_alive "$_pid"; then
		log "sshcustomd running (API still warming up)"
		return 0
	fi
	log "ERROR: sshcustomd failed to start"
	rm -f "$PID_DAEMON"
	return 1
}

# ---- enable iptables (TPROXY or REDIRECT) --------------------------------
enable_iptables() {
	log "loading iptables script: $IPT_SCRIPT"

	if [ ! -f "$IPT_SCRIPT" ]; then
		log "ERROR: $IPT_SCRIPT not found"
		return 1
	fi

	# Export variables for the iptables script
	export BOX_DIR CONFIG_DIR RUN_DIR IPT_LOG_FILE

	# Source the iptables functions
	. "$IPT_SCRIPT"

	# Try TPROXY first, fall back to REDIRECT
	if is_tproxy_supported; then
		log "TPROXY available — enabling TPROXY mode"
		enable_tproxy && {
			log "TPROXY mode enabled successfully"
			return 0
		}
		log "TPROXY enable failed, falling back to REDIRECT"
	fi

	log "enabling REDIRECT mode"
	enable_redirect && {
		log "REDIRECT mode enabled successfully"
		return 0
	}

	log "ERROR: failed to enable any iptables mode"
	return 1
}

# ---- disable iptables ----------------------------------------------------
disable_iptables() {
	log "disabling iptables rules"

	if [ -f "$IPT_SCRIPT" ]; then
		export BOX_DIR CONFIG_DIR RUN_DIR IPT_LOG_FILE
		. "$IPT_SCRIPT"
		disable
	else
		# Fallback: basic cleanup without sourcing
		for _t in mangle nat; do
			for _c in SSHC_TPROXY_PRE SSHC_TPROXY_OUT SSHC_REDIR_PRE SSHC_REDIR_OUT; do
				iptables -t "$_t" -D PREROUTING -j "$_c" 2>/dev/null || true
				iptables -t "$_t" -D OUTPUT -j "$_c" 2>/dev/null || true
				iptables -t "$_t" -F "$_c" 2>/dev/null || true
				iptables -t "$_t" -X "$_c" 2>/dev/null || true
			done
		done
		ip rule del fwmark 0x1 table 100 2>/dev/null || true
		ip route del local 0.0.0.0/0 dev lo table 100 2>/dev/null || true
	fi

	log "iptables rules disabled"
}

# ---- START ---------------------------------------------------------------
do_start() {
	log "========== START v3.1.0 =========="
	mkdir -p "$RUN_DIR"

	# Reset log
	: > "$CORE_LOG"
	log "core.log initialized for new session"

	# 1. Speed boost
	log "Step 1/4: speed_boost"
	if [ -f "$IPT_SCRIPT" ]; then
		export BOX_DIR CONFIG_DIR RUN_DIR IPT_LOG_FILE
		. "$IPT_SCRIPT"
		speed_boost
	fi

	# 2. Start hev-socks5-tproxy
	log "Step 2/4: hev-socks5-tproxy"
	start_hev || { log "FATAL: hev-socks5-tproxy failed"; return 1; }

	# 3. Start sshcustomd daemon
	log "Step 3/4: sshcustomd"
	start_daemon || { log "FATAL: sshcustomd failed"; return 1; }

	# 4. Enable iptables
	log "Step 4/4: iptables enable"
	enable_iptables || { log "FATAL: iptables failed"; return 1; }

	# Mark enabled
	touch "$ENABLED_FILE"

	log "========== START complete =========="
	echo "SSHCustom v3.1.0 started successfully"
	echo "WebUI: http://127.0.0.1:9190/"
	return 0
}

# ---- STOP ----------------------------------------------------------------
do_stop() {
	log "========== STOP =========="

	rm -f "$ENABLED_FILE"

	# 1. Disable iptables (remove redirections first)
	log "Step 1/3: iptables disable"
	disable_iptables

	# 2. Kill sshcustomd
	log "Step 2/3: stop sshcustomd"
	kill_tracked "$PID_DAEMON" "sshcustomd"
	kill_by_name "sshcustomd" "$BIN_DAEMON"

	# 3. Kill hev-socks5-tproxy
	log "Step 3/3: stop hev-socks5-tproxy"
	kill_tracked "$PID_HEV" "hev-socks5-tproxy"
	kill_by_name "hev-socks5-tproxy" "$BIN_HEV"

	# Belt-and-suspenders cleanup
	if [ -f "$IPT_SCRIPT" ]; then
		export BOX_DIR CONFIG_DIR RUN_DIR IPT_LOG_FILE
		. "$IPT_SCRIPT"
		disable
	fi

	log "========== STOP complete =========="
	echo "SSHCustom v3.1.0 stopped"
	return 0
}

# ---- STATUS --------------------------------------------------------------
do_status() {
	_daemon_pid=""
	_hev_pid=""
	[ -f "$PID_DAEMON" ] && _daemon_pid="$(cat "$PID_DAEMON" 2>/dev/null)"
	[ -f "$PID_HEV" ] && _hev_pid="$(cat "$PID_HEV" 2>/dev/null)"

	echo "SSHCustom v3.1.0 status:"
	echo "  work_dir: $BOX_DIR"

	if pid_alive "$_daemon_pid"; then
		echo "  sshcustomd: running (pid=$_daemon_pid)"
	else
		echo "  sshcustomd: stopped"
	fi

	if pid_alive "$_hev_pid"; then
		echo "  hev-socks5-tproxy: running (pid=$_hev_pid)"
	else
		echo "  hev-socks5-tproxy: stopped"
	fi

	if api_ready; then
		echo "  WebUI API: online (http://127.0.0.1:9190/)"
	else
		echo "  WebUI API: offline"
	fi

	[ -f "$ENABLED_FILE" ] && echo "  enabled: yes" || echo "  enabled: no"
	return 0
}

# ---- RESTART -------------------------------------------------------------
do_restart() {
	log "========== RESTART =========="
	do_stop
	sleep 2
	do_start
}

# ---- dispatch ------------------------------------------------------------
case "$1" in
	start)
		do_start
		;;
	stop)
		do_stop
		;;
	restart)
		do_restart
		;;
	status)
		do_status
		;;
	*)
		echo "Usage: $0 {start|stop|restart|status}"
		exit 2
		;;
esac
