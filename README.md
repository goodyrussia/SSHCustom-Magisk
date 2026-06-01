# SSHCustom-Magisk

One-click transparent SSH tunnel VPN for Magisk/KernelSU. Works like HTTP Injector/Custom — paste payload, connect, entire system tunneled. No VpnService. Any rooted Android, any ROM.

## Architecture

```
kernel TPROXY → hev-socks5-tproxy → SOCKS5 → sshcustomd → SSH server
                                              ↓
                                         WebUI :9190
                                         Payload engine
```

## Features

- **One-click transparent proxy** — entire system traffic through SSH tunnel
- **Payload injection engine** — paste payloads from HTTP Injector/Custom
- **TPROXY** — full TCP+UDP support, DNS through tunnel
- **REDIRECT fallback** — works on any kernel
- **WebUI** — manage profiles, payloads, connection from browser
- **Battery efficient** — no VpnService overhead

## Requirements

- Magisk 24+ or KernelSU
- Android 7+
- Root access
- SSH server (bug-host or VPS)

## Quick Start

1. Flash the module in Magisk/KernelSU
2. Reboot
3. Open http://127.0.0.1:9190
4. Add a profile with your SSH server details
5. Click Connect

## Credits

Built with:
- [hev-socks5-tproxy](https://github.com/heiher/hev-socks5-tproxy) — TPROXY bridge (MIT)
- [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) — SSH library

## License

Apache 2.0
