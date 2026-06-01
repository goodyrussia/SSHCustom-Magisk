#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Uninstall Script
# Kills all processes, emergency cleanup, removes /data/adb/sshcustom.
#=============================================================================

BOX_DIR="/data/adb/sshcustom"

echo "SSHCustom-Magisk v3.1.0 uninstall"

# Kill all running processes
echo "- Stopping all processes..."
killall sshcustomd 2>/dev/null || true
killall hev-socks5-tproxy 2>/dev/null || true

# Wait for processes to die
sleep 2

# Force kill any survivors
for _p in $(busybox pidof sshcustomd 2>/dev/null); do
	kill -KILL "$_p" 2>/dev/null || true
done
for _p in $(busybox pidof hev-socks5-tproxy 2>/dev/null); do
	kill -KILL "$_p" 2>/dev/null || true
done

# Run emergency net cleanup
echo "- Cleaning iptables rules..."
if [ -x "${BOX_DIR}/scripts/net_clean.sh" ]; then
	"${BOX_DIR}/scripts/net_clean.sh" >/dev/null 2>&1 || true
fi

# Remove the module data directory
echo "- Removing ${BOX_DIR}..."
rm -rf "$BOX_DIR"

echo "SSHCustom-Magisk uninstalled"
exit 0
