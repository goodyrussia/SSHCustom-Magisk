// Package api provides HTTP REST handlers for the SSHCustom daemon.
package api

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/goodyrussia/SSHCustom-Magisk/internal/config"
	issh "github.com/goodyrussia/SSHCustom-Magisk/internal/ssh"
	"github.com/goodyrussia/SSHCustom-Magisk/internal/version"
	"gopkg.in/yaml.v3"
)

// DaemonState holds the shared mutable state used by the API handlers.
type DaemonState struct {
	Connected    bool
	Reconnecting bool
	TunnelStart  time.Time
	StartedAt    time.Time
	LastError    string
	ActiveConns  int32
	MemMB        float64
	CPUPct       float64
	CurrentProfile string
	TproxyPort   int
	DNSPort      int
	RoutingMode  string
}

// snapshot returns a point-in-time copy of the state.
func (s *DaemonState) snapshot() StatusResponse {
	uptime := int64(0)
	if s.Connected && !s.TunnelStart.IsZero() {
		uptime = int64(time.Since(s.TunnelStart).Seconds())
	}
	return StatusResponse{
		Connected:         s.Connected,
		Running:           s.Connected,
		Reconnecting:      s.Reconnecting,
		Profile:           s.CurrentProfile,
		SSHHost:           "",
		SSHMode:           "",
		MemMB:             s.MemMB,
		CPUPct:            s.CPUPct,
		UptimeSeconds:     uptime,
		ActiveConnections: int(atomic.LoadInt32(&s.ActiveConns)),
		APIPort:           0,
		SocksPort:         0,
		TproxyPort:        s.TproxyPort,
		DNSPort:           s.DNSPort,
		RoutingMode:       s.RoutingMode,
		Version:           version.Version,
		LastError:         s.LastError,
	}
}

// Server holds all dependencies for the HTTP API.
type Server struct {
	ConfigPath    string
	ProfilesPath  string
	AtomicConfig  *config.AtomicConfig
	State         *DaemonState
	SSHClientPtr  *atomic.Pointer[issh.Client]
	TunnelStartFn func(profileName string) error // called by tunnel/start
	TunnelStopFn  func() error                   // called by tunnel/stop
}

// RegisterRoutes adds all API routes to the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/config", s.handleConfig)
	mux.HandleFunc("/api/v1/profiles", s.handleProfiles)
	mux.HandleFunc("/api/v1/profile", s.handleCurrentProfile)
	mux.HandleFunc("/api/v1/tunnel/start", s.handleTunnelStart)
	mux.HandleFunc("/api/v1/tunnel/stop", s.handleTunnelStop)
	mux.HandleFunc("/api/v1/latency", s.handleLatency)
}

// corsHeaders adds CORS headers for the WebUI.
func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// writeJSON writes a JSON response with CORS headers.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// handleStatus returns the current daemon status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}

	cfg := s.AtomicConfig.Get()
	snap := s.State.snapshot()

	// Fill in SSH details and port info from current config
	snap.SSHHost = cfg.SSHHost
	snap.SSHMode = cfg.SSHMode
	snap.APIPort = cfg.APIPort
	snap.SocksPort = cfg.SocksPort

	// Populate TPROXY/DNS ports and routing mode from tproxy.yaml if available
	snap.TproxyPort, snap.DNSPort, snap.RoutingMode = s.loadTproxyConfig()

	writeJSON(w, 200, snap)
}

// tproxyYaml represents the tproxy.yaml configuration structure
type tproxyYaml struct {
	TCP struct {
		Port int `yaml:"port"`
	} `yaml:"tcp"`
	DNS struct {
		Port int `yaml:"port"`
	} `yaml:"dns"`
}

// loadTproxyConfig reads tproxy.yaml and returns TproxyPort, DNSPort, and RoutingMode
func (s *Server) loadTproxyConfig() (int, int, string) {
	configDir := filepath.Dir(s.ConfigPath)
	tproxyPath := filepath.Join(configDir, "tproxy.yaml")
	
	data, err := os.ReadFile(tproxyPath)
	if err != nil {
		return 1088, 1053, "TPROXY"
	}
	
	var cfg tproxyYaml
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return 1088, 1053, "TPROXY"
	}
	
	tproxyPort := cfg.TCP.Port
	if tproxyPort == 0 {
		tproxyPort = 1088
	}
	dnsPort := cfg.DNS.Port
	if dnsPort == 0 {
		dnsPort = 1053
	}
	
	return tproxyPort, dnsPort, "TPROXY"
}

// handleConfig handles GET/PUT /api/v1/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.AtomicConfig.Get()
		resp := ConfigResponse{
			SSHHost:     cfg.SSHHost,
			SSHPort:     cfg.SSHPort,
			SSHUser:     cfg.SSHUser,
			SSHPassword: cfg.SSHPassword,
			SSHMode:     cfg.SSHMode,
			SocksPort:   cfg.SocksPort,
			APIPort:     cfg.APIPort,
			WorkDir:     cfg.WorkDir,
		}
		writeJSON(w, 200, resp)

	case http.MethodPut:
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		// Merge with existing to preserve derived fields
		existing := s.AtomicConfig.Get()
		merged := *existing
		if cfg.SSHHost != "" {
			merged.SSHHost = cfg.SSHHost
		}
		if cfg.SSHPort > 0 {
			merged.SSHPort = cfg.SSHPort
		}
		if cfg.SSHUser != "" {
			merged.SSHUser = cfg.SSHUser
		}
		if cfg.SSHPassword != "" {
			merged.SSHPassword = cfg.SSHPassword
		}
		if cfg.SSHMode != "" {
			merged.SSHMode = cfg.SSHMode
		}
		if cfg.SocksPort > 0 {
			merged.SocksPort = cfg.SocksPort
		}
		if cfg.APIPort > 0 {
			merged.APIPort = cfg.APIPort
		}
		if cfg.WorkDir != "" {
			merged.WorkDir = cfg.WorkDir
		}

		if err := config.SaveConfig(s.ConfigPath, &merged); err != nil {
			writeError(w, 500, "save failed: "+err.Error())
			return
		}
		s.AtomicConfig.Set(&merged)
		writeJSON(w, 200, map[string]bool{"ok": true})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// handleProfiles handles GET/PUT /api/v1/profiles.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}

	switch r.Method {
	case http.MethodGet:
		pf, err := config.LoadProfiles(s.ProfilesPath)
		if err != nil {
			writeError(w, 500, "load profiles: "+err.Error())
			return
		}
		items := make([]ProfileItem, len(pf.Profiles))
		for i, p := range pf.Profiles {
			items[i] = ProfileItem{
				ID:                  p.ID,
				Name:                p.Name,
				SSHHost:             p.SSHHost,
				SSHPort:             p.SSHPort,
				SSHUser:             p.SSHUser,
				SSHPassword:         p.SSHPassword,
				SSHMode:             p.SSHMode,
				SSHSNIHost:          p.SSHSNIHost,
				HTTPProxyHost:       p.HTTPProxyHost,
				HTTPProxyPort:       p.HTTPProxyPort,
				PayloadEnabled:      p.PayloadEnabled,
				Payload:             p.Payload,
				PayloadInjectionType: p.PayloadInjectionType,
				PayloadMethod:       p.PayloadMethod,
				PayloadFrontQuery:   p.PayloadFrontQuery,
				PayloadBackQuery:    p.PayloadBackQuery,
				PayloadDualConnect:  p.PayloadDualConnect,
				PayloadSplit:        p.PayloadSplit,
			PayloadUA:           p.PayloadUA,
			}
		}
		writeJSON(w, 200, ProfilesResponse{
			SelectedID: pf.SelectedID,
			Profiles:   items,
		})

	case http.MethodPut:
		var req ProfilesResponse
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		pf := &config.ProfilesFile{
			SelectedID: req.SelectedID,
			Profiles:   make([]config.Profile, len(req.Profiles)),
		}
		for i, item := range req.Profiles {
			pf.Profiles[i] = config.Profile{
				ID:                  item.ID,
				Name:                item.Name,
				SSHHost:             item.SSHHost,
				SSHPort:             item.SSHPort,
				SSHUser:             item.SSHUser,
				SSHPassword:         item.SSHPassword,
				SSHMode:             item.SSHMode,
				SSHSNIHost:          item.SSHSNIHost,
				HTTPProxyHost:       item.HTTPProxyHost,
				HTTPProxyPort:       item.HTTPProxyPort,
				PayloadEnabled:      item.PayloadEnabled,
				Payload:             item.Payload,
				PayloadInjectionType: item.PayloadInjectionType,
				PayloadMethod:       item.PayloadMethod,
				PayloadFrontQuery:   item.PayloadFrontQuery,
				PayloadBackQuery:    item.PayloadBackQuery,
				PayloadDualConnect:  item.PayloadDualConnect,
				PayloadSplit:        item.PayloadSplit,
			PayloadUA:           item.PayloadUA,
			}
		}
		// Validate SelectedID exists in profiles list
		if req.SelectedID != "" {
			found := false
			for _, p := range pf.Profiles {
				if p.ID == req.SelectedID {
					found = true
					break
				}
			}
			if !found {
				writeError(w, 400, "selected_id does not exist in profiles list")
				return
			}
		}
		if err := config.SaveProfiles(s.ProfilesPath, pf); err != nil {
			writeError(w, 500, "save profiles: "+err.Error())
			return
		}
		// If the current profile changed, update state
		if pf.SelectedID != "" {
			s.State.CurrentProfile = pf.SelectedID
			// Merge profile into config
			if profile := config.GetCurrentProfile(pf); profile != nil {
				newCfg := config.ConfigFromProfile(s.AtomicConfig.Get(), profile)
				s.AtomicConfig.Set(newCfg)
			}
		}
		writeJSON(w, 200, map[string]bool{"ok": true})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// handleCurrentProfile returns the currently selected profile.
func (s *Server) handleCurrentProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}

	pf, err := config.LoadProfiles(s.ProfilesPath)
	if err != nil {
		writeError(w, 500, "load profiles: "+err.Error())
		return
	}

	profile := config.GetCurrentProfile(pf)
	if profile == nil {
		writeJSON(w, 200, map[string]interface{}{"selected_id": "", "profile": nil})
		return
	}

	item := ProfileItem{
		ID:                  profile.ID,
		Name:                profile.Name,
		SSHHost:             profile.SSHHost,
		SSHPort:             profile.SSHPort,
		SSHUser:             profile.SSHUser,
		SSHPassword:         profile.SSHPassword,
		SSHMode:             profile.SSHMode,
		SSHSNIHost:          profile.SSHSNIHost,
		HTTPProxyHost:       profile.HTTPProxyHost,
		HTTPProxyPort:       profile.HTTPProxyPort,
		PayloadEnabled:      profile.PayloadEnabled,
		Payload:             profile.Payload,
		PayloadInjectionType: profile.PayloadInjectionType,
		PayloadMethod:       profile.PayloadMethod,
		PayloadFrontQuery:   profile.PayloadFrontQuery,
		PayloadBackQuery:    profile.PayloadBackQuery,
		PayloadDualConnect:  profile.PayloadDualConnect,
		PayloadSplit:        profile.PayloadSplit,
		PayloadUA:           profile.PayloadUA,
	}
	writeJSON(w, 200, map[string]interface{}{
		"selected_id": pf.SelectedID,
		"profile":     item,
	})
}

// handleTunnelStart starts the SSH tunnel.
func (s *Server) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}

	var req TunnelRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON: "+err.Error())
			return
		}
	}

	// If a specific profile is requested, update the current profile
	if req.Profile != "" {
		pf, err := config.LoadProfiles(s.ProfilesPath)
		if err != nil {
			writeError(w, 500, "load profiles: "+err.Error())
			return
		}
		pf.SelectedID = req.Profile
		if err := config.SaveProfiles(s.ProfilesPath, pf); err != nil {
			writeError(w, 500, "save profile: "+err.Error())
			return
		}
		s.State.CurrentProfile = req.Profile
		if profile := config.GetCurrentProfile(pf); profile != nil {
			newCfg := config.ConfigFromProfile(s.AtomicConfig.Get(), profile)
			s.AtomicConfig.Set(newCfg)
		}
	}

	if s.TunnelStartFn != nil {
		if err := s.TunnelStartFn(req.Profile); err != nil {
			writeJSON(w, 500, TunnelResponse{OK: false, Error: err.Error()})
			return
		}
	}

	writeJSON(w, 200, TunnelResponse{OK: true, Message: "tunnel starting"})
}

// handleTunnelStop stops the SSH tunnel.
func (s *Server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}

	if s.TunnelStopFn != nil {
		if err := s.TunnelStopFn(); err != nil {
			writeJSON(w, 500, TunnelResponse{OK: false, Error: err.Error()})
			return
		}
	}

	writeJSON(w, 200, TunnelResponse{OK: true, Message: "tunnel stopping"})
}

// handleLatency tests latency to the SSH host.
func (s *Server) handleLatency(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}

	host := r.URL.Query().Get("host")
	portStr := r.URL.Query().Get("port")
	if host == "" {
		writeError(w, 400, "host parameter required")
		return
	}
	port := 22
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	start := time.Now()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, 200, LatencyResponse{
			Host:    host,
			Port:    port,
			Latency: -1,
			Error:   err.Error(),
		})
		return
	}
	conn.Close()

	writeJSON(w, 200, LatencyResponse{
		Host:    host,
		Port:    port,
		Latency: elapsed,
	})
}


