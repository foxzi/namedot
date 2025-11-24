package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		expectedError string
		description   string
	}{
		{
			name: "valid minimal config",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "",
			description:   "Should accept minimal valid config",
		},
		{
			name: "valid config with all fields",
			config: &Config{
				Listen:           "127.0.0.1:5353",
				Forwarder:        "8.8.8.8",
				RESTListen:       "127.0.0.1:8081",
				EnableDNSSEC:     true,
				APIToken:         "test-token-123",
				DefaultTTL:       300,
				SOA:              SOAConfig{AutoOnMissing: true},
				DB: DBConfig{
					Driver: "postgres",
					DSN:    "host=localhost dbname=namedot",
				},
				Performance: PerformanceConfig{
					CacheSize:           2048,
					DNSTimeoutSec:       5,
					ForwarderTimeoutSec: 5,
				},
			},
			expectedError: "",
			description:   "Should accept fully configured valid config",
		},
		{
			name: "invalid listen address - missing port",
			config: &Config{
				Listen:     "0.0.0.0",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "invalid listen address",
			description:   "Should reject address without port",
		},
		{
			name: "invalid listen address - invalid port",
			config: &Config{
				Listen:     "0.0.0.0:99999",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "port must be between 1 and 65535",
			description:   "Should reject port > 65535",
		},
		{
			name: "invalid listen address - port 0",
			config: &Config{
				Listen:     "0.0.0.0:0",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "port must be between 1 and 65535",
			description:   "Should reject port 0",
		},
		{
			name: "invalid REST listen address",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "invalid:port",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "invalid rest_listen address",
			description:   "Should reject invalid REST address",
		},
		{
			name: "invalid forwarder address",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				Forwarder:  "invalid forwarder",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "invalid forwarder address",
			description:   "Should reject forwarder with spaces",
		},
		{
			name: "missing DB driver",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "",
					DSN:    ":memory:",
				},
			},
			expectedError: "db.driver is required",
			description:   "Should require DB driver",
		},
		{
			name: "missing DB DSN",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    "",
				},
			},
			expectedError: "db.dsn is required",
			description:   "Should require DB DSN",
		},
		{
			name: "GeoIP enabled without mmdb_path",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				GeoIP: GeoIPConfig{
					Enabled:  true,
					MMDBPath: "",
				},
			},
			expectedError: "geoip.mmdb_path is required when geoip is enabled",
			description:   "Should require mmdb_path when GeoIP enabled",
		},
		{
			name: "negative cache size",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Performance: PerformanceConfig{
					CacheSize:           -1,
					DNSTimeoutSec:       2,
					ForwarderTimeoutSec: 2,
				},
			},
			expectedError: "performance.cache_size must be >= 0",
			description:   "Should reject negative cache size",
		},
		{
			name: "zero DNS timeout",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Performance: PerformanceConfig{
					CacheSize:           1024,
					DNSTimeoutSec:       0,
					ForwarderTimeoutSec: 2,
				},
			},
			expectedError: "performance.dns_timeout_sec must be > 0",
			description:   "Should reject zero DNS timeout",
		},
		{
			name: "zero forwarder timeout",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Performance: PerformanceConfig{
					CacheSize:           1024,
					DNSTimeoutSec:       2,
					ForwarderTimeoutSec: 0,
				},
			},
			expectedError: "performance.forwarder_timeout_sec must be > 0",
			description:   "Should reject zero forwarder timeout",
		},
		{
			name: "both api_token and api_token_hash",
			config: &Config{
				Listen:       "0.0.0.0:53",
				RESTListen:   "0.0.0.0:8080",
				APIToken:     "plain-token",
				APITokenHash: "$2a$10$hashedtoken",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "cannot specify both api_token and api_token_hash",
			description:   "Should reject both token and hash",
		},
		{
			name: "invalid replication mode",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Replication: ReplicationConfig{
					Mode: "invalid",
				},
			},
			expectedError: "replication.mode must be 'master', 'slave', 'standalone', or empty",
			description:   "Should reject invalid replication mode",
		},
		{
			name: "slave mode without master_url",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Replication: ReplicationConfig{
					Mode: "slave",
				},
			},
			expectedError: "replication.master_url is required when replication.mode is 'slave'",
			description:   "Should require master_url in slave mode",
		},
		{
			name: "slave mode without sync_interval_sec",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Replication: ReplicationConfig{
					Mode:            "slave",
					MasterURL:       "http://master:8080",
					SyncIntervalSec: 0,
				},
			},
			expectedError: "replication.sync_interval_sec must be > 0 when replication.mode is 'slave'",
			description:   "Should require positive sync interval in slave mode",
		},
		{
			name: "valid master replication config",
			config: &Config{
				Listen:     "0.0.0.0:53",
				RESTListen: "0.0.0.0:8080",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
				Replication: ReplicationConfig{
					Mode: "master",
				},
			},
			expectedError: "",
			description:   "Should accept master mode without extra config",
		},
		{
			name: "TLS cert without key",
			config: &Config{
				Listen:      "0.0.0.0:53",
				RESTListen:  "0.0.0.0:8080",
				TLSCertFile: "/path/to/cert.pem",
				TLSKeyFile:  "",
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "both tls_cert_file and tls_key_file must be specified together",
			description:   "Should require both TLS cert and key",
		},
		{
			name: "invalid CIDR",
			config: &Config{
				Listen:       "0.0.0.0:53",
				RESTListen:   "0.0.0.0:8080",
				AllowedCIDRs: []string{"192.168.1.0/24", "invalid-cidr"},
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "allowed_cidrs[1]: invalid CIDR",
			description:   "Should validate all CIDRs",
		},
		{
			name: "valid CIDRs",
			config: &Config{
				Listen:       "0.0.0.0:53",
				RESTListen:   "0.0.0.0:8080",
				AllowedCIDRs: []string{"192.168.1.0/24", "10.0.0.0/8", "2001:db8::/32"},
				DB: DBConfig{
					Driver: "sqlite",
					DSN:    ":memory:",
				},
			},
			expectedError: "",
			description:   "Should accept valid IPv4 and IPv6 CIDRs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply defaults for fields that Load() would set
			// But skip defaults for tests that explicitly test zero/negative values
			if tt.config.Performance.CacheSize == 0 && !strings.Contains(tt.name, "negative cache") {
				tt.config.Performance.CacheSize = 1024
			}
			if tt.config.Performance.DNSTimeoutSec == 0 && !strings.Contains(tt.name, "zero DNS timeout") {
				tt.config.Performance.DNSTimeoutSec = 2
			}
			if tt.config.Performance.ForwarderTimeoutSec == 0 && !strings.Contains(tt.name, "zero forwarder timeout") {
				tt.config.Performance.ForwarderTimeoutSec = 2
			}

			err := tt.config.Validate()

			if tt.expectedError == "" {
				if err != nil {
					t.Errorf("%s\nExpected no error, got: %v", tt.description, err)
				}
			} else {
				if err == nil {
					t.Errorf("%s\nExpected error containing '%s', got no error", tt.description, tt.expectedError)
				} else if !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("%s\nExpected error containing '%s', got: %v", tt.description, tt.expectedError, err)
				}
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	minimalYAML := `
listen: ":53"
db:
  driver: sqlite
  dsn: ":memory:"
`

	if err := os.WriteFile(configPath, []byte(minimalYAML), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check defaults
	if cfg.RESTListen != ":8080" {
		t.Errorf("Expected default RESTListen ':8080', got '%s'", cfg.RESTListen)
	}
	if cfg.Performance.CacheSize != 1024 {
		t.Errorf("Expected default CacheSize 1024, got %d", cfg.Performance.CacheSize)
	}
	if cfg.Performance.DNSTimeoutSec != 2 {
		t.Errorf("Expected default DNSTimeoutSec 2, got %d", cfg.Performance.DNSTimeoutSec)
	}
	if cfg.Performance.ForwarderTimeoutSec != 2 {
		t.Errorf("Expected default ForwarderTimeoutSec 2, got %d", cfg.Performance.ForwarderTimeoutSec)
	}
}

func TestConfigLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	invalidYAML := `
listen: ":53"
db:
  driver: sqlite
  invalid yaml here
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("Expected error for invalid YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parse yaml") {
		t.Errorf("Expected 'parse yaml' error, got: %v", err)
	}
}

func TestConfigLoad_NonExistentFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Errorf("Expected 'read config' error, got: %v", err)
	}
}

func TestIsTLSEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected bool
	}{
		{
			name: "both cert and key set",
			config: &Config{
				TLSCertFile: "/path/to/cert.pem",
				TLSKeyFile:  "/path/to/key.pem",
			},
			expected: true,
		},
		{
			name: "only cert set",
			config: &Config{
				TLSCertFile: "/path/to/cert.pem",
				TLSKeyFile:  "",
			},
			expected: false,
		},
		{
			name: "only key set",
			config: &Config{
				TLSCertFile: "",
				TLSKeyFile:  "/path/to/key.pem",
			},
			expected: false,
		},
		{
			name: "neither set",
			config: &Config{
				TLSCertFile: "",
				TLSKeyFile:  "",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsTLSEnabled()
			if result != tt.expected {
				t.Errorf("Expected IsTLSEnabled() = %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestHasIPACL(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected bool
	}{
		{
			name: "with CIDRs",
			config: &Config{
				AllowedCIDRs: []string{"192.168.1.0/24"},
			},
			expected: true,
		},
		{
			name: "without CIDRs",
			config: &Config{
				AllowedCIDRs: []string{},
			},
			expected: false,
		},
		{
			name:     "nil CIDRs",
			config:   &Config{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.HasIPACL()
			if result != tt.expected {
				t.Errorf("Expected HasIPACL() = %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestValidateAddr(t *testing.T) {
	tests := []struct {
		addr          string
		shouldBeValid bool
		description   string
	}{
		{"0.0.0.0:53", true, "Valid IPv4 with port"},
		{"127.0.0.1:8080", true, "Valid localhost with port"},
		{"[::1]:53", true, "Valid IPv6 with port"},
		{"[2001:db8::1]:8080", true, "Valid IPv6 address with port"},
		{":53", true, "Valid all interfaces with port"},
		{"localhost:8080", true, "Valid hostname with port"},
		{"0.0.0.0", false, "Missing port"},
		{"0.0.0.0:99999", false, "Port too high"},
		{"0.0.0.0:0", false, "Port zero"},
		{"0.0.0.0:-1", false, "Negative port"},
		{"0.0.0.0:abc", false, "Non-numeric port"},
		{"invalid host:53", false, "Host with spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := validateAddr(tt.addr)
			isValid := err == nil

			if isValid != tt.shouldBeValid {
				t.Errorf("%s: Expected valid=%v, got valid=%v (error: %v)",
					tt.description, tt.shouldBeValid, isValid, err)
			}
		})
	}
}

func TestValidateHost(t *testing.T) {
	tests := []struct {
		host          string
		shouldBeValid bool
		description   string
	}{
		{"8.8.8.8", true, "Valid IPv4"},
		{"2001:db8::1", true, "Valid IPv6"},
		{"localhost", true, "Valid hostname"},
		{"dns.google.com", true, "Valid FQDN"},
		{"", false, "Empty host"},
		{"invalid host", false, "Host with spaces"},
		{"host with spaces", false, "Multiple spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			err := validateHost(tt.host)
			isValid := err == nil

			if isValid != tt.shouldBeValid {
				t.Errorf("%s: Expected valid=%v, got valid=%v (error: %v)",
					tt.description, tt.shouldBeValid, isValid, err)
			}
		})
	}
}

func TestSlaveMode_AutoDisablesAdmin(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "slave.yaml")

	slaveYAML := `
listen: ":53"
db:
  driver: sqlite
  dsn: ":memory:"
admin:
  enabled: true
  username: admin
  password_hash: $2a$10$test
replication:
  mode: slave
  master_url: http://master:8080
  sync_interval_sec: 60
`

	if err := os.WriteFile(configPath, []byte(slaveYAML), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Admin.Enabled {
		t.Error("Expected admin to be auto-disabled in slave mode, but it's still enabled")
	}
}
