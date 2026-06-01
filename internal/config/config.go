// Package config handles config.json and profiles.json loading/saving with
// atomic swaps for concurrent access.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
)

// Config holds the daemon's runtime config from config.json.
// All fields use FLAT json tags — no nesting.
type Config struct {
	SSHHost     string `json:"ssh_host"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password"`
	SSHMode     string `json:"ssh_mode"` // direct | sni | sni_http_proxy

	SocksPort int `json:"socks_port"`
	APIPort   int `json:"api_port"`
	WorkDir   string `json:"work_dir"`

	// Derived from the selected profile (loaded from profiles.json).
	// These are populated by ConfigFromProfile().
	SSHSNIHost           string `json:"ssh_sni_host,omitempty"`
	HTTPProxyHost        string `json:"http_proxy_host,omitempty"`
	HTTPProxyPort        int    `json:"http_proxy_port,omitempty"`
	PayloadEnabled       bool   `json:"payload_enabled"`
	Payload              string `json:"payload,omitempty"`
	PayloadInjectionType string `json:"payload_injection_type,omitempty"`
	PayloadMethod        string `json:"payload_method,omitempty"`
	PayloadFrontQuery    bool   `json:"payload_front_query"`
	PayloadBackQuery     bool   `json:"payload_back_query"`
	PayloadDualConnect   bool   `json:"payload_dual_connect"`
	PayloadSplit         bool   `json:"payload_split"`
	PayloadUA            string `json:"payload_ua,omitempty"`
}

// Profile holds a single profile from profiles.json.
// All fields use FLAT json tags — no nesting.
type Profile struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`

	SSHHost             string `json:"ssh_host"`
	SSHPort             int    `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	SSHPassword         string `json:"ssh_password"`
	SSHMode             string `json:"ssh_mode"` // direct | sni | sni_http_proxy
	SSHSNIHost          string `json:"ssh_sni_host,omitempty"`
	HTTPProxyHost       string `json:"http_proxy_host,omitempty"`
	HTTPProxyPort       int    `json:"http_proxy_port,omitempty"`
	PayloadEnabled      bool   `json:"payload_enabled"`
	Payload             string `json:"payload,omitempty"`
	PayloadInjectionType string `json:"payload_injection_type,omitempty"` // normal | front | back | front_query | back_query
	PayloadMethod       string `json:"payload_method,omitempty"`         // CONNECT | GET | POST
	PayloadFrontQuery   bool   `json:"payload_front_query"`
	PayloadBackQuery    bool   `json:"payload_back_query"`
	PayloadDualConnect  bool   `json:"payload_dual_connect"`
	PayloadSplit        bool   `json:"payload_split"`
}

// ProfilesFile is the structure of profiles.json.
type ProfilesFile struct {
	SelectedID string    `json:"selected_id"`
	Profiles   []Profile `json:"profiles"`
}

// AtomicConfig provides lock-free atomic reads of the current config.
type AtomicConfig struct {
	ptr atomic.Pointer[Config]
}

// NewAtomic creates an AtomicConfig from an initial config.
func NewAtomic(c *Config) *AtomicConfig {
	a := &AtomicConfig{}
	a.ptr.Store(c)
	return a
}

// Get returns the current config snapshot.
func (a *AtomicConfig) Get() *Config {
	return a.ptr.Load()
}

// Set atomically replaces the current config.
func (a *AtomicConfig) Set(c *Config) {
	a.ptr.Store(c)
}

// DefaultConfig returns a Config with safe defaults.
func DefaultConfig() *Config {
	return &Config{
		SSHPort:   22,
		SSHMode:   "direct",
		SocksPort: 1080,
		APIPort:   9190,
		WorkDir:   "/data/adb/sshcustom",
	}
}

// DefaultProfiles returns an empty profiles file.
func DefaultProfiles() *ProfilesFile {
	return &ProfilesFile{
		SelectedID: "",
		Profiles:   []Profile{},
	}
}

// LoadConfig reads config.json from path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("config load: %w", err)
	}
	c := DefaultConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	return c, nil
}

// SaveConfig writes the config to path.
func SaveConfig(path string, c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("config write: %w", err)
	}
	return nil
}

// LoadProfiles reads profiles.json from path.
func LoadProfiles(path string) (*ProfilesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultProfiles(), nil
		}
		return nil, fmt.Errorf("profiles load: %w", err)
	}
	pf := DefaultProfiles()
	if err := json.Unmarshal(data, pf); err != nil {
		return nil, fmt.Errorf("profiles parse: %w", err)
	}
	return pf, nil
}

// SaveProfiles writes the profiles to path.
func SaveProfiles(path string, pf *ProfilesFile) error {
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("profiles write: %w", err)
	}
	return nil
}

// GetCurrentProfile returns the currently selected profile from profiles.json,
// or nil if no profile is selected.
func GetCurrentProfile(pf *ProfilesFile) *Profile {
	for i := range pf.Profiles {
		if pf.Profiles[i].ID == pf.SelectedID {
			return &pf.Profiles[i]
		}
	}
	return nil
}

// ConfigFromProfile derives a tunnel-ready Config by merging the base config
// with the selected profile's SSH and payload settings.
func ConfigFromProfile(base *Config, profile *Profile) *Config {
	if profile == nil {
		return base
	}
	c := *base // shallow copy

	// Override SSH settings from profile
	if profile.SSHHost != "" {
		c.SSHHost = profile.SSHHost
	}
	if profile.SSHPort > 0 {
		c.SSHPort = profile.SSHPort
	}
	if profile.SSHUser != "" {
		c.SSHUser = profile.SSHUser
	}
	if profile.SSHPassword != "" {
		c.SSHPassword = profile.SSHPassword
	}
	if profile.SSHMode != "" {
		c.SSHMode = profile.SSHMode
	}

	// Transport settings
	c.SSHSNIHost = profile.SSHSNIHost
	c.HTTPProxyHost = profile.HTTPProxyHost
	c.HTTPProxyPort = profile.HTTPProxyPort

	// Payload settings
	c.PayloadEnabled = profile.PayloadEnabled
	c.Payload = profile.Payload
	c.PayloadInjectionType = profile.PayloadInjectionType
	c.PayloadMethod = profile.PayloadMethod
	c.PayloadFrontQuery = profile.PayloadFrontQuery
	c.PayloadBackQuery = profile.PayloadBackQuery
	c.PayloadDualConnect = profile.PayloadDualConnect
	c.PayloadSplit = profile.PayloadSplit

	return &c
}
