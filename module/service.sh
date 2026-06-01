#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Late-Start Service
# Waits for sys.boot_completed=1, then extra sleep for network, then starts.
#=============================================================================

BOX_DIR="/data/adb/sshcustom"
RUN_DIR="${BOX_DIR}/run"
SCRIPTS_DIR="${BOX_DIR}/scripts"
BOOT_LOG="${RUN_DIR}/boot.log"

mkdir -p "$RUN_DIR"

{
	echo "$(date '+%Y-%m-%d %H:%M:%S') ========== boot service v3.1.0 =========="

	# Wait for Android boot to fully complete
	echo "$(date '+%Y-%m-%d %H:%M:%S') waiting for sys.boot_completed=1..."
	while true; do
		_bc="$(getprop sys.boot_completed 2>/dev/null)"
		[ "$_bc" = "1" ] && break
		sleep 5
	done
	echo "$(date '+%Y-%m-%d %H:%M:%S') boot completed"

	# Extra sleep to let network interfaces come up
	echo "$(date '+%Y-%m-%d %H:%M:%S') waiting 10s for network..."
	sleep 10
	echo "$(date '+%Y-%m-%d %H:%M:%S') network wait done"

	# Start SSHCustom
	if [ -x "${SCRIPTS_DIR}/sshcustom.sh" ]; then
		echo "$(date '+%Y-%m-%d %H:%M:%S') starting sshcustom.sh"
		"${SCRIPTS_DIR}/sshcustom.sh" start
		echo "$(date '+%Y-%m-%d %H:%M:%S') sshcustom.sh exited with code $?"
	else
		echo "$(date '+%Y-%m-%d %H:%M:%S') ERROR: ${SCRIPTS_DIR}/sshcustom.sh not found or not executable"
	fi

	echo "$(date '+%Y-%m-%d %H:%M:%S') ========== boot service done =========="
} >> "$BOOT_LOG" 2>&1

exit 0
