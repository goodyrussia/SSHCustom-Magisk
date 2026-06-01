// sshcustomd — SSHCustom-Magisk v3.1.0 daemon
//
// Single static binary: GOOS=android GOARCH=arm64 CGO_ENABLED=0.
//
// Usage:
//
//	sshcustomd -c /data/adb/sshcustom/config.json -p /data/adb/sshcustom/profiles.json --api-addr 127.0.0.1:9190
//
// The daemon starts in idle mode (SOCKS5 + WebUI API running, no SSH tunnel).
// The tunnel is started via the API (POST /api/v1/tunnel/start) or the WebUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/goodyrussia/SSHCustom-Magisk/internal/api"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/config"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/metrics"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/proxy"
	issh "github.com/goodyrussia/SSHCustom-Magisk/internal/ssh"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/transport"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/version"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/webui"
)

func main() {
	// GC tuning
	debug.SetGCPercent(200)
	debug.SetMemoryLimit(128 * 1024 * 1024)

	cfgPath := flag.String("c", "/data/adb/sshcustom/config.json", "path to config.json")
	profilesPath := flag.String("p", "/data/adb/sshcustom/profiles.json", "path to profiles.json")
	apiAddr := flag.String("api-addr", "127.0.0.1:9190", "API listen address")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("[main] sshcustomd v%s %s/%s starting", version.Version, runtime.GOOS, runtime.GOARCH)

	// Load config
	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("[main] config load: %v", err)
	}
	atomicCfg := config.NewAtomic(cfg)

	// Load profiles
	profiles, err := config.LoadProfiles(*profilesPath)
	if err != nil {
		log.Printf("[main] profiles load: %v (using defaults)", err)
		profiles = config.DefaultProfiles()
	}

	// If a profile is selected, merge it into the config
	currentProfile := config.GetCurrentProfile(profiles)
	if currentProfile != nil {
		cfg = config.ConfigFromProfile(cfg, currentProfile)
		atomicCfg.Set(cfg)
	}

	// Ensure work directory exists
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "/data/adb/sshcustom"
	}
	os.MkdirAll(workDir, 0700)

	// Shared state
	st := &api.DaemonState{
		StartedAt:      time.Now(),
		CurrentProfile: profiles.Current,
	}

	var sshClient atomic.Pointer[issh.Client]

	// Tunnel control
	var (
		tunnelCancel context.CancelFunc
		tunnelMu     sync.Mutex
	)

	startTunnel := func(profileName string) error {
		tunnelMu.Lock()
		defer tunnelMu.Unlock()

		// If already running, stop first
		if tunnelCancel != nil {
			tunnelCancel()
			// Wait briefly for cleanup
			time.Sleep(100 * time.Millisecond)
		}

		// Reload config + profiles to get the latest
		currentCfg := atomicCfg.Get()
		pf, err := config.LoadProfiles(*profilesPath)
		if err == nil {
			if profileName != "" {
				pf.Current = profileName
				config.SaveProfiles(*profilesPath, pf)
				st.CurrentProfile = profileName
			}
			profile := config.GetCurrentProfile(pf)
			if profile != nil {
				currentCfg = config.ConfigFromProfile(currentCfg, profile)
				atomicCfg.Set(currentCfg)
			}
		}

		if currentCfg.SSHHost == "" || currentCfg.SSHUser == "" {
			return fmt.Errorf("ssh_host and ssh_user must be configured")
		}

		tctx, tcancel := context.WithCancel(context.Background())
		tunnelCancel = tcancel

		go runTunnel(tctx, currentCfg, st, &sshClient)
		return nil
	}

	stopTunnel := func() error {
		tunnelMu.Lock()
		defer tunnelMu.Unlock()

		if tunnelCancel != nil {
			tunnelCancel()
			tunnelCancel = nil
		}

		// Close existing SSH client
		if c := sshClient.Load(); c != nil {
			c.Close()
			sshClient.Store(nil)
		}

		st.Connected = false
		st.LastError = ""
		return nil
	}

	// SOCKS5 server (always running)
	socksCtx, socksCancel := context.WithCancel(context.Background())
	defer socksCancel()

	socksSrv := &proxy.SOCKS5Server{
		Addr: fmt.Sprintf("127.0.0.1:%d", cfg.SocksPort),
		Client: func() *issh.Client {
			return sshClient.Load()
		},
	}
	go func() {
		if err := socksSrv.ListenAndServe(socksCtx); err != nil {
			log.Printf("[socks5] %v", err)
		}
	}()

	// HTTP API + WebUI
	apiSrv := &api.Server{
		ConfigPath:    *cfgPath,
		ProfilesPath:  *profilesPath,
		AtomicConfig:  atomicCfg,
		State:         st,
		SSHClientPtr:  &sshClient,
		TunnelStartFn: startTunnel,
		TunnelStopFn:  stopTunnel,
	}

	mux := http.NewServeMux()
	apiSrv.RegisterRoutes(mux)

	// WebUI handler (disk-first, fallback HTML)
	webHandler := webui.NewHandler(workDir)
	mux.Handle("/", webHandler)

	httpSrv := &http.Server{
		Addr:         *apiAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Printf("[http] listening on %s", *apiAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[http] %v", err)
		}
	}()

	// Metrics loop
	go metricsLoop(context.Background(), st, &sshClient)

	// Signal handling
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for {
		s := <-sig
		switch s {
		case syscall.SIGHUP:
			log.Println("[main] SIGHUP — reloading config")
			newCfg, err := config.LoadConfig(*cfgPath)
			if err != nil {
				log.Printf("[main] config reload failed: %v", err)
			} else {
				atomicCfg.Set(newCfg)
				log.Println("[main] config reloaded")
			}
		default:
			log.Printf("[main] signal %v — shutting down", s)
			stopTunnel()
			socksCancel()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutCancel()
			httpSrv.Shutdown(shutCtx)
			time.Sleep(300 * time.Millisecond)
			return
		}
	}
}

// runTunnel connects the SSH tunnel and blocks until the context is cancelled
// or the connection dies. It handles reconnection with backoff.
func runTunnel(
	ctx context.Context,
	cfg *config.Config,
	st *api.DaemonState,
	clientPtr *atomic.Pointer[issh.Client],
) {
	const (
		baseDelay = 1 * time.Second
		maxDelay  = 30 * time.Second
	)
	delay := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Build connect config
		mode := issh.TransportMode(cfg.SSHMode)
		if mode == "" {
			mode = issh.ModeDirect
		}

		connCfg := issh.ConnectConfig{
			Host:              cfg.SSHHost,
			Port:              cfg.SSHPort,
			User:              cfg.SSHUser,
			Password:          cfg.SSHPassword,
			Mode:              mode,
			SNIHost:           cfg.SSHSNIHost,
			HTTPProxyHost:     cfg.HTTPProxyHost,
			HTTPProxyPort:     cfg.HTTPProxyPort,
			PayloadEnabled:    cfg.PayloadEnabled,
			Payload:           cfg.Payload,
			ConnectTimeout:    25 * time.Second,
			KeepAliveInterval: 30 * time.Second,
			KeepAliveMax:      3,
		}

		log.Printf("[tunnel] connecting to %s:%d mode=%s", cfg.SSHHost, cfg.SSHPort, cfg.SSHMode)
		st.LastError = ""

		dialCtx, dialCancel := context.WithTimeout(ctx, 25*time.Second)
		c, err := issh.Dial(dialCtx, connCfg)
		dialCancel()

		if err != nil {
			log.Printf("[tunnel] connect failed: %v", err)
			st.Connected = false
			st.LastError = err.Error()
			delay = nextDelay(delay, baseDelay, maxDelay)
			continue
		}

		// Connected
		clientPtr.Store(c)
		st.Connected = true
		st.TunnelStart = time.Now()
		st.LastError = ""
		log.Printf("[tunnel] connected to %s:%d", cfg.SSHHost, cfg.SSHPort)

		// Wait for disconnection
		waitErr := c.Wait()
		log.Printf("[tunnel] connection lost: %v", waitErr)
		c.Close()
		clientPtr.Store(nil)
		st.Connected = false
		st.LastError = ""

		delay = baseDelay

		// Check if context is done before reconnecting
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// nextDelay returns the next backoff delay.
func nextDelay(cur, base, max time.Duration) time.Duration {
	if cur <= 0 {
		return base
	}
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// metricsLoop updates the daemon state with resource usage periodically.
func metricsLoop(ctx context.Context, st *api.DaemonState, clientPtr *atomic.Pointer[issh.Client]) {
	var sampler metrics.Sampler
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			samp := sampler.Sample()
			st.MemMB = samp.RSSMB
			st.CPUPct = samp.CPUPct
			if c := clientPtr.Load(); c != nil {
				atomic.StoreInt32(&st.ActiveConns, int32(c.ActiveConns()))
			} else {
				atomic.StoreInt32(&st.ActiveConns, 0)
			}
		}
	}
}

// Ensure transport is imported.
var _ = transport.WritePayload
