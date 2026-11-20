package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

type DBConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type GeoIPConfig struct {
	Enabled             bool     `yaml:"enabled"`
	MMDBPath            string   `yaml:"mmdb_path"`
	ReloadSec           int      `yaml:"reload_sec"`
	UseECS              bool     `yaml:"use_ecs"`
	DownloadURLs        []string `yaml:"download_urls"`
	DownloadIntervalSec int      `yaml:"download_interval_sec"`
}

type LogConfig struct {
	DNSVerbose bool `yaml:"dns_verbose"`
	SQLDebug   bool `yaml:"sql_debug"`
}

type PerformanceConfig struct {
	CacheSize           int `yaml:"cache_size"`
	DNSTimeoutSec       int `yaml:"dns_timeout_sec"`
	ForwarderTimeoutSec int `yaml:"forwarder_timeout_sec"`
}

type AdminConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"` // bcrypt hash
}

type ReplicationConfig struct {
	Mode            string `yaml:"mode"`              // "master", "slave", "standalone", or "" (disabled)
	MasterURL       string `yaml:"master_url"`        // URL of master server (for slave mode)
	SyncIntervalSec int    `yaml:"sync_interval_sec"` // Sync interval in seconds (for slave mode)
	APIToken        string `yaml:"api_token"`         // API token for master authentication
}

// APITokenConfig is a named REST API token; the name is shown in API logs.
type APITokenConfig struct {
	Name string `yaml:"name"`
	Hash string `yaml:"hash"` // bcrypt hash
}

type SOAConfig struct {
	Primary       string `yaml:"primary"`         // MNAME (e.g. ns1.{zone})
	Hostmaster    string `yaml:"hostmaster"`      // RNAME (e.g. hostmaster.{zone})
	AutoOnMissing bool   `yaml:"auto_on_missing"` // Auto-create SOA when missing
}

type NSConfig struct {
	Servers       []string `yaml:"servers"`         // NS server names (e.g. ["ns1.{zone}", "ns2.{zone}"])
	AutoOnMissing bool     `yaml:"auto_on_missing"` // Auto-create NS on zone creation
	AutoFix       bool     `yaml:"auto_fix"`        // Auto-fix missing NS on server startup
	TTL           uint32   `yaml:"ttl"`             // TTL for NS records (default 86400)
}

type Config struct {
	Listen       string           `yaml:"listen"`
	Forwarder    string           `yaml:"forwarder"`
	EnableDNSSEC bool             `yaml:"enable_dnssec"`
	APIToken     string           `yaml:"api_token"`      // Plain text token (deprecated, use api_token_hash)
	APITokenHash string           `yaml:"api_token_hash"` // bcrypt hash of token (recommended)
	APITokens    []APITokenConfig `yaml:"api_tokens"`     // Named tokens (bcrypt hashes), may be combined with api_token_hash
	RESTListen   string           `yaml:"rest_listen"`
	TLSCertFile  string           `yaml:"tls_cert_file"`  // Path to TLS certificate file for HTTPS
	TLSKeyFile   string           `yaml:"tls_key_file"`   // Path to TLS private key file for HTTPS
	TLSReloadSec int              `yaml:"tls_reload_sec"` // Certificate reload interval in seconds (0 = no reload)
	AllowedCIDRs []string         `yaml:"allowed_cidrs"`  // List of allowed CIDR blocks for REST API access (empty = allow all)
	DefaultTTL   uint32           `yaml:"default_ttl"`
	SOA          SOAConfig        `yaml:"soa"`
	NS           NSConfig         `yaml:"ns"`
	// Deprecated: use soa.auto_on_missing instead
	AutoSOAOnMissing bool `yaml:"auto_soa_on_missing"`

	DB          DBConfig          `yaml:"db"`
	GeoIP       GeoIPConfig       `yaml:"geoip"`
	Log         LogConfig         `yaml:"log"`
	Performance PerformanceConfig `yaml:"performance"`
	Admin       AdminConfig       `yaml:"admin"`
	Replication ReplicationConfig `yaml:"replication"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	// Apply defaults
	if cfg.RESTListen == "" {
		cfg.RESTListen = ":8080"
	}
	if cfg.Listen == "" {
		cfg.Listen = ":53"
	}
	if cfg.Performance.CacheSize == 0 {
		cfg.Performance.CacheSize = 1024
	}
	if cfg.Performance.DNSTimeoutSec == 0 {
		cfg.Performance.DNSTimeoutSec = 2
	}
	if cfg.Performance.ForwarderTimeoutSec == 0 {
		cfg.Performance.ForwarderTimeoutSec = 2
	}
	if cfg.Replication.SyncIntervalSec == 0 && cfg.Replication.Mode == "slave" {
		cfg.Replication.SyncIntervalSec = 60 // Default: 60 seconds
	}
	if cfg.TLSReloadSec == 0 && cfg.IsTLSEnabled() {
		cfg.TLSReloadSec = 3600 // Default: 3600 seconds (1 hour)
	}
	if !cfg.SOA.AutoOnMissing && cfg.AutoSOAOnMissing {
		cfg.SOA.AutoOnMissing = true // backward compatibility for deprecated root field
	}
	if cfg.NS.TTL == 0 {
		cfg.NS.TTL = 86400 // Default NS TTL: 24 hours
	}

	// Auto-disable modifications on slave servers
	if cfg.Replication.Mode == "slave" {
		if cfg.Admin.Enabled {
			fmt.Fprintf(os.Stderr, "INFO: Admin panel automatically disabled in slave mode\n")
			cfg.Admin.Enabled = false
		}
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, nil
}

// Validate checks configuration for correctness
func (c *Config) Validate() error {
	// Validate DNS listen address
	if err := validateAddr(c.Listen); err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}

	// Validate REST listen address
	if err := validateAddr(c.RESTListen); err != nil {
		return fmt.Errorf("invalid rest_listen address: %w", err)
	}

	// Validate forwarder if set
	if c.Forwarder != "" {
		if err := validateHost(c.Forwarder); err != nil {
			return fmt.Errorf("invalid forwarder address: %w", err)
		}
	}

	// Validate DB config
	if c.DB.Driver == "" {
		return fmt.Errorf("db.driver is required")
	}
	if c.DB.DSN == "" {
		return fmt.Errorf("db.dsn is required")
	}

	// Validate GeoIP config
	if c.GeoIP.Enabled && c.GeoIP.MMDBPath == "" {
		return fmt.Errorf("geoip.mmdb_path is required when geoip is enabled")
	}
	if c.GeoIP.Enabled && c.GeoIP.MMDBPath != "" {
		// If auto-download is configured, create directory if it doesn't exist
		if len(c.GeoIP.DownloadURLs) > 0 && c.GeoIP.DownloadIntervalSec > 0 {
			if err := os.MkdirAll(c.GeoIP.MMDBPath, 0755); err != nil {
				return fmt.Errorf("geoip.mmdb_path: failed to create directory: %w", err)
			}
		} else {
			// No auto-download, require existing directory
			if _, err := os.Stat(c.GeoIP.MMDBPath); err != nil {
				return fmt.Errorf("geoip.mmdb_path: %w", err)
			}
		}
	}

	// Validate performance limits
	if c.Performance.CacheSize < 0 {
		return fmt.Errorf("performance.cache_size must be >= 0")
	}
	if c.Performance.DNSTimeoutSec <= 0 {
		return fmt.Errorf("performance.dns_timeout_sec must be > 0")
	}
	if c.Performance.ForwarderTimeoutSec <= 0 {
		return fmt.Errorf("performance.forwarder_timeout_sec must be > 0")
	}

	// Validate API token configuration
	if c.APIToken != "" && c.APITokenHash != "" {
		return fmt.Errorf("cannot specify both api_token and api_token_hash, use only api_token_hash (recommended)")
	}

	// Validate named API tokens ("default" is the name of the legacy tokens)
	seenTokens := make(map[string]bool)
	for i, t := range c.APITokens {
		if t.Name == "" {
			return fmt.Errorf("api_tokens[%d]: name is required", i)
		}
		if t.Name == "default" {
			return fmt.Errorf("api_tokens[%d]: name \"default\" is reserved for api_token/api_token_hash", i)
		}
		if seenTokens[t.Name] {
			return fmt.Errorf("api_tokens[%d]: duplicate name %q", i, t.Name)
		}
		seenTokens[t.Name] = true
		if t.Hash == "" {
			return fmt.Errorf("api_tokens[%d]: hash is required", i)
		}
		if _, err := bcrypt.Cost([]byte(t.Hash)); err != nil {
			return fmt.Errorf("api_tokens[%d]: hash is not a valid bcrypt hash: %w", i, err)
		}
	}

	// Warn if using plain text token (deprecated)
	if c.APIToken != "" {
		fmt.Fprintf(os.Stderr, "WARNING: api_token (plain text) is deprecated, consider using api_token_hash instead\n")
		if len(c.APIToken) < 8 {
			fmt.Fprintf(os.Stderr, "WARNING: api_token is shorter than 8 characters, consider using a stronger token\n")
		}
	}

	// Validate replication config
	if c.Replication.Mode != "" && c.Replication.Mode != "master" && c.Replication.Mode != "slave" && c.Replication.Mode != "standalone" {
		return fmt.Errorf("replication.mode must be 'master', 'slave', 'standalone', or empty (got '%s')", c.Replication.Mode)
	}
	if c.Replication.Mode == "slave" {
		if c.Replication.MasterURL == "" {
			return fmt.Errorf("replication.master_url is required when replication.mode is 'slave'")
		}
		if c.Replication.SyncIntervalSec <= 0 {
			return fmt.Errorf("replication.sync_interval_sec must be > 0 when replication.mode is 'slave'")
		}
	}

	// Validate TLS config
	if (c.TLSCertFile != "" && c.TLSKeyFile == "") || (c.TLSCertFile == "" && c.TLSKeyFile != "") {
		return fmt.Errorf("both tls_cert_file and tls_key_file must be specified together")
	}
	if c.TLSCertFile != "" && c.TLSKeyFile != "" {
		if _, err := os.Stat(c.TLSCertFile); err != nil {
			return fmt.Errorf("tls_cert_file: %w", err)
		}
		if _, err := os.Stat(c.TLSKeyFile); err != nil {
			return fmt.Errorf("tls_key_file: %w", err)
		}
	}

	// Validate allowed CIDRs
	for i, cidr := range c.AllowedCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("allowed_cidrs[%d]: invalid CIDR %q: %w", i, cidr, err)
		}
	}

	return nil
}

// IsTLSEnabled returns true if TLS is configured for REST API
func (c *Config) IsTLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// HasIPACL returns true if IP ACL is configured
func (c *Config) HasIPACL() bool {
	return len(c.AllowedCIDRs) > 0
}

// validateAddr validates host:port address format
func validateAddr(addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}

	// Validate port
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}

	// Validate host (empty means all interfaces, which is valid)
	if host != "" {
		if ip := net.ParseIP(host); ip == nil {
			// Not an IP, could be hostname - allow it
			if strings.Contains(host, " ") {
				return fmt.Errorf("invalid host: contains spaces")
			}
		}
	}

	return nil
}

// validateHost validates IP or hostname (without port)
func validateHost(addr string) error {
	// Check if it's an IP
	if ip := net.ParseIP(addr); ip != nil {
		return nil
	}

	// Check if it's a valid hostname
	if addr == "" || strings.Contains(addr, " ") {
		return fmt.Errorf("invalid hostname")
	}

	return nil
}
