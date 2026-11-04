package geoip

import (
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewNoop(t *testing.T) {
	provider := NewNoop()
	if provider == nil {
		t.Fatal("Expected noop provider, got nil")
	}

	// Test that it implements Provider interface
	var _ Provider = provider
}

func TestNoopProvider_Lookup(t *testing.T) {
	provider := NewNoop()

	testIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
		"::1",
		"192.168.1.1",
	}

	for _, ipStr := range testIPs {
		t.Run(ipStr, func(t *testing.T) {
			ip := netip.MustParseAddr(ipStr)
			info := provider.Lookup(ip)

			if info.Country != "" {
				t.Errorf("Expected empty country for noop provider, got '%s'", info.Country)
			}
			if info.Continent != "" {
				t.Errorf("Expected empty continent for noop provider, got '%s'", info.Continent)
			}
			if info.ASN != 0 {
				t.Errorf("Expected zero ASN for noop provider, got %d", info.ASN)
			}
		})
	}
}

func TestContinentFromCountry(t *testing.T) {
	tests := []struct {
		countryCode       string
		expectedContinent string
		description       string
	}{
		// North America
		{"US", "NA", "United States should map to NA"},
		{"CA", "NA", "Canada should map to NA"},
		{"MX", "NA", "Mexico should map to NA"},

		// Europe
		{"GB", "EU", "United Kingdom should map to EU"},
		{"FR", "EU", "France should map to EU"},
		{"DE", "EU", "Germany should map to EU"},
		{"IT", "EU", "Italy should map to EU"},
		{"ES", "EU", "Spain should map to EU"},
		{"NL", "EU", "Netherlands should map to EU"},
		{"RU", "EU", "Russia should map to EU"},

		// Asia
		{"CN", "AS", "China should map to AS"},
		{"JP", "AS", "Japan should map to AS"},
		{"IN", "AS", "India should map to AS"},
		{"KR", "AS", "South Korea should map to AS"},
		{"SG", "AS", "Singapore should map to AS"},

		// South America
		{"BR", "SA", "Brazil should map to SA"},
		{"AR", "SA", "Argentina should map to SA"},
		{"CL", "SA", "Chile should map to SA"},

		// Oceania
		{"AU", "OC", "Australia should map to OC"},
		{"NZ", "OC", "New Zealand should map to OC"},

		// Africa
		{"ZA", "AF", "South Africa should map to AF"},
		{"EG", "AF", "Egypt should map to AF"},
		{"NG", "AF", "Nigeria should map to AF"},

		// Fallback cases - countries not in the explicit map
		{"AZ", "AS", "Azerbaijan should fallback to AS (A prefix)"},
		{"AF", "AS", "Afghanistan should fallback to AS (A prefix)"},
		{"BO", "SA", "Bolivia should fallback to SA (B prefix)"},
		{"PL", "EU", "Poland should fallback to EU (P prefix)"},

		// Edge cases
		{"", "", "Empty code should return empty"},
		{"X", "", "Single character should return empty"},
		{"ZZ", "", "Unknown code should return empty"},
	}

	for _, tt := range tests {
		t.Run(tt.countryCode, func(t *testing.T) {
			result := continentFromCountry(tt.countryCode)
			if result != tt.expectedContinent {
				t.Errorf("%s: Expected continent '%s', got '%s'",
					tt.description, tt.expectedContinent, result)
			}
		})
	}
}

func TestContinentFromCountry_Fallback(t *testing.T) {
	// Test fallback logic for unmapped countries
	tests := []struct {
		countryCode    string
		expectedPrefix string
		description    string
	}{
		{"DK", "EU", "Denmark (D prefix) should fallback to Europe"},
		{"FI", "EU", "Finland (F prefix) should fallback to Europe"},
		{"GR", "EU", "Greece (G prefix) should fallback to Europe"},
		{"HU", "EU", "Hungary (H prefix) should fallback to Europe"},
		{"IE", "EU", "Ireland (I prefix) should fallback to Europe"},
		{"LT", "EU", "Lithuania (L prefix) should fallback to Europe"},
		{"NO", "EU", "Norway (N prefix) should fallback to Europe"},
		{"PT", "EU", "Portugal (P prefix) should fallback to Europe"},
		{"RO", "EU", "Romania (R prefix) should fallback to Europe"},
		{"SE", "EU", "Sweden (S prefix) should fallback to Europe"},
		{"TR", "EU", "Turkey (T prefix) should fallback to Europe"},
	}

	for _, tt := range tests {
		t.Run(tt.countryCode, func(t *testing.T) {
			result := continentFromCountry(tt.countryCode)
			if result != tt.expectedPrefix {
				t.Errorf("%s: Expected '%s', got '%s'",
					tt.description, tt.expectedPrefix, result)
			}
		})
	}
}

func TestInfo_Struct(t *testing.T) {
	// Test Info struct field access
	info := Info{
		Country:   "US",
		Continent: "NA",
		ASN:       15169,
	}

	if info.Country != "US" {
		t.Errorf("Expected Country 'US', got '%s'", info.Country)
	}
	if info.Continent != "NA" {
		t.Errorf("Expected Continent 'NA', got '%s'", info.Continent)
	}
	if info.ASN != 15169 {
		t.Errorf("Expected ASN 15169, got %d", info.ASN)
	}
}

func TestInfo_EmptyValues(t *testing.T) {
	// Test zero-value Info
	info := Info{}

	if info.Country != "" {
		t.Errorf("Expected empty Country, got '%s'", info.Country)
	}
	if info.Continent != "" {
		t.Errorf("Expected empty Continent, got '%s'", info.Continent)
	}
	if info.ASN != 0 {
		t.Errorf("Expected zero ASN, got %d", info.ASN)
	}
}

func TestNewFromPath_NonExistentPath(t *testing.T) {
	provider, cleanup, err := NewFromPath("/nonexistent/path", 0, nil, 0)
	defer cleanup()

	if err == nil {
		t.Error("Expected error for non-existent path, got nil")
	}

	// Should return noop provider as fallback
	if provider == nil {
		t.Fatal("Expected noop provider fallback, got nil")
	}

	// Verify it's a working provider (even if noop)
	info := provider.Lookup(netip.MustParseAddr("8.8.8.8"))
	_ = info // Should not panic
}

func TestNewFromPath_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	provider, cleanup, err := NewFromPath(tmpDir, 0, nil, 0)
	defer cleanup()

	if err == nil {
		t.Error("Expected error for empty directory, got nil")
	}

	// Should return noop provider as fallback
	if provider == nil {
		t.Fatal("Expected noop provider fallback, got nil")
	}
}

func BenchmarkNoopProvider_Lookup(b *testing.B) {
	provider := NewNoop()
	ip := netip.MustParseAddr("8.8.8.8")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.Lookup(ip)
	}
}

func BenchmarkContinentFromCountry(b *testing.B) {
	countries := []string{"US", "GB", "CN", "BR", "AU", "ZA"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		countryCode := countries[i%len(countries)]
		continentFromCountry(countryCode)
	}
}

func BenchmarkContinentFromCountry_Fallback(b *testing.B) {
	// Test fallback performance
	countries := []string{"XY", "ZZ", "QW", "VB", "KL"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		countryCode := countries[i%len(countries)]
		continentFromCountry(countryCode)
	}
}

func TestNewFromPath_ReloadDuringLookups(t *testing.T) {
	dir := filepath.Join("..", "..", "geoipdb")
	p, stop, err := NewFromPath(dir, time.Millisecond, nil, 0)
	if err != nil {
		t.Skipf("geoipdb not available: %v", err)
	}
	defer stop()

	ip := netip.MustParseAddr("127.0.1.10")
	want := p.Lookup(ip)
	if want.Country != "RU" || want.ASN != 65001 {
		t.Fatalf("unexpected lookup result for %s: %+v", ip, want)
	}

	var wg sync.WaitGroup
	deadline := time.Now().Add(300 * time.Millisecond)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if got := p.Lookup(ip); got != want {
					t.Errorf("lookup during reload: got %+v, want %+v", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}
