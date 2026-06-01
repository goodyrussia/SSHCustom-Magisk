// Package api defines the REST API contracts (request/response types).
package api

// StatusResponse is returned by GET /api/v1/status.
type StatusResponse struct {
	Connected         bool    `json:"connected"`
	Profile           string  `json:"profile"`
	SSHHost           string  `json:"ssh_host"`
	SSHMode           string  `json:"ssh_mode"`
	MemMB             float64 `json:"mem_mb"`
	CPUPct            float64 `json:"cpu_pct"`
	UptimeSeconds     int64   `json:"uptime_seconds"`
	ActiveConnections int     `json:"active_connections"`
	Version           string  `json:"version"`
	LastError         string  `json:"last_error,omitempty"`
}

// ConfigResponse mirrors config.Config for the JSON API.
type ConfigResponse struct {
	SSHHost     string `json:"ssh_host"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password"`
	SSHMode     string `json:"ssh_mode"`
	SocksPort   int    `json:"socks_port"`
	APIPort     int    `json:"api_port"`
	WorkDir     string `json:"work_dir"`
}

// ProfileItem mirrors config.Profile for the JSON API.
type ProfileItem struct {
	Name                string `json:"name"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int    `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	SSHPassword         string `json:"ssh_password"`
	SSHMode             string `json:"ssh_mode"`
	SSHSNIHost          string `json:"ssh_sni_host,omitempty"`
	HTTPProxyHost       string `json:"http_proxy_host,omitempty"`
	HTTPProxyPort       int    `json:"http_proxy_port,omitempty"`
	PayloadEnabled      bool   `json:"payload_enabled"`
	Payload             string `json:"payload,omitempty"`
	PayloadInjectionType string `json:"payload_injection_type,omitempty"`
	PayloadMethod       string `json:"payload_method,omitempty"`
	PayloadFrontQuery   bool   `json:"payload_front_query"`
	PayloadBackQuery    bool   `json:"payload_back_query"`
	PayloadDualConnect  bool   `json:"payload_dual_connect"`
	PayloadSplit        bool   `json:"payload_split"`
}

// ProfilesResponse is returned by GET /api/v1/profiles.
type ProfilesResponse struct {
	Current string        `json:"current"`
	Items   []ProfileItem `json:"items"`
}

// TunnelRequest is the body of POST /api/v1/tunnel/start.
type TunnelRequest struct {
	Profile string `json:"profile,omitempty"`
}

// TunnelResponse is returned by tunnel start/stop.
type TunnelResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// LatencyResponse is returned by GET /api/v1/latency.
type LatencyResponse struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Latency int64  `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

// ErrorResponse is a generic error.
type ErrorResponse struct {
	Error string `json:"error"`
}
