#!/bin/bash
set -e
cd "$(dirname "$0")"
VER=$(cat VERSION)
echo "=== SSHCustom-Magisk v${VER} ==="

# 1. Go daemon
echo "[1/6] Compiling sshcustomd..."
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/goodyrussia/SSHCustom-Magisk/internal/version.Version=${VER}" -o module/bin/arm64-v8a/sshcustomd ./cmd/sshcustomd/
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/goodyrussia/SSHCustom-Magisk/internal/version.Version=${VER}" -o module/bin/armeabi-v7a/sshcustomd ./cmd/sshcustomd/
echo "  arm64: $(file module/bin/arm64-v8a/sshcustomd | cut -d, -f1)"
echo "  armv7: $(file module/bin/armeabi-v7a/sshcustomd | cut -d, -f1)"

# 2. vet
echo "[2/6] go vet..."
go vet ./... 2>&1 || true

# 3. hev-socks5-tproxy binaries (pre-downloaded)
echo "[3/6] Verifying hev-socks5-tproxy..."
for arch in arm64-v8a armeabi-v7a; do
  if [ -f "module/bin/${arch}/hev-socks5-tproxy" ]; then
    echo "  ${arch}: $(file module/bin/${arch}/hev-socks5-tproxy | cut -d, -f1)"
  else
    echo "  ${arch}: MISSING — downloading..."
    exit 1
  fi
done

# 4. syntax check scripts
echo "[4/6] Syntax check..."
bash -n module/customize.sh && echo "  customize.sh OK"
sh -n module/scripts/sshcustom.sh && echo "  sshcustom.sh OK"
sh -n module/scripts/sshcustom.iptables && echo "  sshcustom.iptables OK"
sh -n module/scripts/net_clean.sh && echo "  net_clean.sh OK"
sh -n module/service.sh && echo "  service.sh OK"

# 5. package
echo "[5/6] Packaging..."
ZIP="SSHCustom-Magisk-v${VER}.zip"
rm -f "dist/${ZIP}"
cd module
zip -r "../dist/${ZIP}" . -x "*.git*" "*.DS_Store" >/dev/null
cd ..
echo "  dist/${ZIP} ($(stat -c%s "dist/${ZIP}") bytes)"

# 6. done
echo "[6/6] Build complete!"
ls -la dist/
