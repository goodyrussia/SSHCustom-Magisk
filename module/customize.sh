#!/system/bin/sh
#=============================================================================
# SSHCustom-Magisk v3.1.0 — Magisk Install Script (customize.sh)
# MODDIR uses cd+dirname+pwd — NO ".." anywhere
#=============================================================================

SKIPMOUNT=false
PROPFILE=false
POSTFSDATA=false
LATESTARTSERVICE=true

# MODDIR is the module directory (where this script lives).
# Using cd+dirname+pwd to resolve the REAL path without "..".
MODDIR="$(cd "$(dirname "$0")" && pwd)"

BOX_DIR="/data/adb/sshcustom"
BIN_DST="${BOX_DIR}/bin"
SCRIPTS_DST="${BOX_DIR}/scripts"
CONFIG_DST="${BOX_DIR}/config"
RUN_DST="${BOX_DIR}/run"
WEBROOT_DST="${BOX_DIR}/webroot"

ui_print "****************************************"
ui_print " SSHCustom-Magisk v3.1.0"
ui_print " TPROXY + hev-socks5-tproxy engine"
ui_print "****************************************"

# ---- Step 1: Detect architecture ----------------------------------------
ARCH=""
ABI="$(getprop ro.product.cpu.abi 2>/dev/null)"
if [ -z "$ABI" ]; then
	ABI="$(getprop ro.product.cpu.abi2 2>/dev/null)"
fi

case "$ABI" in
	arm64-v8a)
		ARCH="arm64"
		ui_print "- Detected ABI: arm64-v8a"
		;;
	armeabi-v7a|armeabi)
		ARCH="arm"
		ui_print "- Detected ABI: armeabi-v7a"
		;;
	*)
		ui_print "ERROR: Unsupported ABI: ${ABI}"
		ui_print "SSHCustom requires arm64-v8a or armeabi-v7a"
		abort "Unsupported architecture"
		;;
esac

# ---- Step 2: Create directory structure ---------------------------------
ui_print "- Creating directory structure at ${BOX_DIR}"
mkdir -p "$BOX_DIR"           2>/dev/null
mkdir -p "$BIN_DST"          2>/dev/null
mkdir -p "$SCRIPTS_DST"      2>/dev/null
mkdir -p "$CONFIG_DST"       2>/dev/null
mkdir -p "$RUN_DST"          2>/dev/null
mkdir -p "$WEBROOT_DST"      2>/dev/null

# ---- Step 3: Copy binaries ----------------------------------------------
SRC_BIN_DIR="${MODDIR}/bin/${ARCH}"
if [ ! -d "$SRC_BIN_DIR" ]; then
	ui_print "ERROR: Binary source directory not found: ${SRC_BIN_DIR}"
	abort "Missing binaries for ${ARCH}"
fi

ui_print "- Installing binaries for ${ARCH}"
for _bin_file in "$SRC_BIN_DIR"/*; do
	[ -f "$_bin_file" ] || continue
	_bin_name="$(basename "$_bin_file")"
	cp -af "$_bin_file" "${BIN_DST}/${_bin_name}"
	chmod 0755 "${BIN_DST}/${_bin_name}"
	ui_print "  + ${_bin_name}"
done

# ---- Step 4: Copy scripts ------------------------------------------------
SRC_SCRIPTS_DIR="${MODDIR}/scripts"
if [ -d "$SRC_SCRIPTS_DIR" ]; then
	ui_print "- Installing scripts"
	for _script_file in "$SRC_SCRIPTS_DIR"/*; do
		[ -f "$_script_file" ] || continue
		_script_name="$(basename "$_script_file")"
		cp -af "$_script_file" "${SCRIPTS_DST}/${_script_name}"
		chmod 0755 "${SCRIPTS_DST}/${_script_name}"
		ui_print "  + scripts/${_script_name}"
	done
fi

# ---- Step 5: Copy config (preserve existing if present) -----------------
SRC_CONFIG_DIR="${MODDIR}/config"
if [ -d "$SRC_CONFIG_DIR" ]; then
	ui_print "- Installing config files"

	# Preserve existing config.json if it exists
	if [ -f "${CONFIG_DST}/config.json" ]; then
		ui_print "  Preserving existing config.json"
	else
		if [ -f "${SRC_CONFIG_DIR}/config.json" ]; then
			cp -af "${SRC_CONFIG_DIR}/config.json" "${CONFIG_DST}/config.json"
			ui_print "  + config/config.json (fresh)"
		fi
	fi

	# Preserve existing profiles.json if it exists
	if [ -f "${CONFIG_DST}/profiles.json" ]; then
		ui_print "  Preserving existing profiles.json"
	else
		if [ -f "${SRC_CONFIG_DIR}/profiles.json" ]; then
			cp -af "${SRC_CONFIG_DIR}/profiles.json" "${CONFIG_DST}/profiles.json"
			ui_print "  + config/profiles.json (fresh)"
		fi
	fi

	# Copy any other config files (always refresh)
	for _cfg_file in "$SRC_CONFIG_DIR"/*; do
		[ -f "$_cfg_file" ] || continue
		_cfg_name="$(basename "$_cfg_file")"
		case "$_cfg_name" in
			config.json|profiles.json)
				# Already handled above
				;;
			*)
				cp -af "$_cfg_file" "${CONFIG_DST}/${_cfg_name}"
				ui_print "  + config/${_cfg_name}"
				;;
		esac
	done
fi

# ---- Step 6: Copy webroot ------------------------------------------------
SRC_WEBROOT_DIR="${MODDIR}/webroot"
if [ -d "$SRC_WEBROOT_DIR" ]; then
	ui_print "- Installing webroot"
	cp -af "$SRC_WEBROOT_DIR"/* "$WEBROOT_DST"/ 2>/dev/null || true
	cp -af "$SRC_WEBROOT_DIR"/. "$WEBROOT_DST"/ 2>/dev/null || true
	ui_print "  + webroot/ installed"
fi

# ---- Step 7: Set global permissions -------------------------------------
ui_print "- Setting permissions"
chmod 0755 "$BOX_DIR" "$BIN_DST" "$SCRIPTS_DST" "$CONFIG_DST" "$RUN_DST" "$WEBROOT_DST"
chmod 0644 "${CONFIG_DST}/config.json" 2>/dev/null || true
chmod 0600 "${CONFIG_DST}/profiles.json" 2>/dev/null || true

# ---- Step 8: Clean stale state from previous run ------------------------
ui_print "- Cleaning stale runtime state"
rm -f "${RUN_DST}/enabled" "${RUN_DST}/network_paused" \
      "${RUN_DST}/daemon.pid" "${RUN_DST}/hev.pid" \
      "${RUN_DST}/watchdog.pid" "${RUN_DST}/core.log" \
      "${RUN_DST}/iptables.log"

# Run emergency cleanup to purge any leftover rules from crash
if [ -x "${SCRIPTS_DST}/net_clean.sh" ]; then
	"${SCRIPTS_DST}/net_clean.sh" >/dev/null 2>&1 || true
fi

# ---- Done ----------------------------------------------------------------
ui_print "****************************************"
ui_print " SSHCustom-Magisk v3.1.0 installed"
ui_print " Architecture: ${ARCH} (${ABI})"
ui_print " Install path: ${BOX_DIR}"
ui_print " WebUI: http://127.0.0.1:9190/"
ui_print " Module will auto-start after boot"
ui_print "****************************************"
