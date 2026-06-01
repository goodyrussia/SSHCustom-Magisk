# Changelog

## v3.1.0 (2026-06-02)

### Complete Rebuild

- **3-process architecture**: hev-socks5-tproxy + sshcustomd + shell iptables
- **Full payload injection engine** — Front/Back/Query/Dual Connect/Split modes
- **TPROXY** with REDIRECT fallback — full TCP+UDP support
- **DNS through tunnel** — eliminates "no internet" issues permanently
- **MMRL-compatible WebUI** — dark theme, profile editor, status dashboard
- **RAM optimized** — target ~13MB idle / ~30-40MB working
- **Shell iptables** — -w 100 wrapper, auto-detect TPROXY/REDIRECT
- **Single SSH connection** — on-demand channels, no pool overhead
