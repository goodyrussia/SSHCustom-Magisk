#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Early Mount (post-fs-data.sh)
# Runs early in boot. Just ensures /data/adb/sshcustom exists.
#=============================================================================
mkdir -p /data/adb/sshcustom 2>/dev/null
exit 0
