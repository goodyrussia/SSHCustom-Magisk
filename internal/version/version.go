// Package version carries the daemon version string, set via -ldflags.
package version

// Version is the current build version. Override with:
//
//	go build -ldflags "-X github.com/goodyrussia/SSHCustom-Magisk/internal/version.Version=3.1.0"
var Version = "3.1.0-dev"
