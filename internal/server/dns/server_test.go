package dns

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"namedot/internal/cache"
	"namedot/internal/config"
	dbm "namedot/internal/db"
	"namedot/internal/geoip"
)

func TestSelectGeoRecords(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.5")
	recs := []dbm.RData{
		{Data: "192.0.2.1"},
		{Data: "192.0.2.2", Subnet: strPtr("203.0.113.0/24")},
		{Data: "192.0.2.3", Country: strPtr("US")},
	}
	out, rule := selectGeoRecords(recs, ip, geoip.Info{})
	if rule != "subnet" {
		t.Fatalf("expected rule subnet, got %s", rule)
	}
	if len(out) != 1 || out[0].Data != "192.0.2.2" {
		t.Fatalf("expected subnet match, got %#v", out)
	}
}

func strPtr(s string) *string { return &s }

// cacheWriter verifies that cached response gets current query ID
type cacheWriter struct{ wrote *uint16 }

func (cw *cacheWriter) WriteMsg(m *dns.Msg) error {
	if cw.wrote != nil {
		*cw.wrote = m.Id
	}
	return nil
}
func (cw *cacheWriter) LocalAddr() net.Addr         { return &net.UDPAddr{} }
func (cw *cacheWriter) RemoteAddr() net.Addr        { return &net.UDPAddr{} }
func (cw *cacheWriter) Write(b []byte) (int, error) { return len(b), nil }
func (cw *cacheWriter) Close() error                { return nil }
func (cw *cacheWriter) TsigStatus() error           { return nil }
func (cw *cacheWriter) TsigTimersOnly(bool)         {}
func (cw *cacheWriter) Hijack()                     {}

func TestCacheResponse_UsesCurrentID(t *testing.T) {
	s := &Server{cache: cache.New(10)}
	// Prepare cached message with old ID
	old := new(dns.Msg)
	old.SetReply(&dns.Msg{MsgHdr: dns.MsgHdr{Id: 111}})
	old.Question = []dns.Question{{Name: "www.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}
	s.cache.Set("www.example.com.|1|", old, time.Minute)

	// Incoming query with new ID
	req := new(dns.Msg)
	req.Id = 222
	req.Question = []dns.Question{{Name: "www.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}

	var got uint16
	cw := &cacheWriter{wrote: &got}
	s.serveDNS(cw, req)
	if got != 222 {
		t.Fatalf("cached response used wrong ID: got %d want 222", got)
	}
}

func TestWildcardNames(t *testing.T) {
	tests := []struct {
		qname, zone string
		want        []string
	}{
		{"foo.bar.example.com.", "example.com.", []string{"*.bar.example.com.", "*.example.com."}},
		{"foo.example.com.", "example.com.", []string{"*.example.com."}},
		{"example.com.", "example.com.", nil},
		{"a.b.c.example.com.", "example.com.", []string{"*.b.c.example.com.", "*.c.example.com.", "*.example.com."}},
	}
	for _, tt := range tests {
		got := wildcardNames(tt.qname, tt.zone)
		if len(got) != len(tt.want) {
			t.Errorf("wildcardNames(%q, %q) = %v, want %v", tt.qname, tt.zone, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("wildcardNames(%q, %q)[%d] = %q, want %q", tt.qname, tt.zone, i, got[i], tt.want[i])
			}
		}
	}
}

func newTestServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&dbm.Zone{}, &dbm.RRSet{}, &dbm.RData{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{Listen: ":0", RESTListen: ":0", Performance: config.PerformanceConfig{CacheSize: 0, ForwarderTimeoutSec: 1}, GeoIP: config.GeoIPConfig{Enabled: false}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return s, db
}

func TestLookup_Wildcard_A(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "192.0.2.99"}}})

	q := dns.Question{Name: "anything.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans, ttl, rule, err := s.lookup(new(dns.Msg), q, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if ttl != 60 {
		t.Fatalf("ttl want 60 got %d", ttl)
	}
	if len(ans) != 1 {
		t.Fatalf("want 1 answer, got %d", len(ans))
	}
	if ans[0].Header().Name != "anything.example.com." {
		t.Fatalf("owner name want anything.example.com. got %s", ans[0].Header().Name)
	}
	if a, ok := ans[0].(*dns.A); !ok || a.A.String() != "192.0.2.99" {
		t.Fatalf("want 192.0.2.99, got %v", ans[0])
	}
	if rule != "wildcard:generic" {
		t.Fatalf("rule want wildcard:generic got %s", rule)
	}
}

func TestLookup_Wildcard_ExactMatchWins(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "192.0.2.99"}}})
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "www.example.com.", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "192.0.2.1"}}})

	// Exact match should win
	q := dns.Question{Name: "www.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans, ttl, rule, err := s.lookup(new(dns.Msg), q, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if ttl != 300 {
		t.Fatalf("ttl want 300 got %d", ttl)
	}
	if a, ok := ans[0].(*dns.A); !ok || a.A.String() != "192.0.2.1" {
		t.Fatalf("want exact 192.0.2.1, got %v", ans[0])
	}
	if rule == "wildcard:generic" {
		t.Fatalf("should not be wildcard match")
	}

	// Other name should match wildcard
	q2 := dns.Question{Name: "other.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans2, _, rule2, err := s.lookup(new(dns.Msg), q2, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if a, ok := ans2[0].(*dns.A); !ok || a.A.String() != "192.0.2.99" {
		t.Fatalf("want wildcard 192.0.2.99, got %v", ans2[0])
	}
	if rule2 != "wildcard:generic" {
		t.Fatalf("rule want wildcard:generic got %s", rule2)
	}
}

func TestLookup_Wildcard_MultiLevel(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.bar.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "10.0.0.1"}}})
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "10.0.0.2"}}})

	// foo.bar.example.com -> *.bar.example.com (more specific)
	q := dns.Question{Name: "foo.bar.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans, _, _, err := s.lookup(new(dns.Msg), q, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if a, ok := ans[0].(*dns.A); !ok || a.A.String() != "10.0.0.1" {
		t.Fatalf("want 10.0.0.1, got %v", ans[0])
	}

	// foo.baz.example.com -> *.example.com (less specific)
	q2 := dns.Question{Name: "foo.baz.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans2, _, _, err := s.lookup(new(dns.Msg), q2, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if a, ok := ans2[0].(*dns.A); !ok || a.A.String() != "10.0.0.2" {
		t.Fatalf("want 10.0.0.2, got %v", ans2[0])
	}
}

func TestLookup_Wildcard_CNAME(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "CNAME", TTL: 120, Records: []dbm.RData{{Data: "fallback.example.net."}}})

	q := dns.Question{Name: "anything.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans, _, rule, err := s.lookup(new(dns.Msg), q, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if len(ans) != 1 {
		t.Fatalf("want 1 answer, got %d", len(ans))
	}
	if ans[0].Header().Rrtype != dns.TypeCNAME {
		t.Fatalf("want CNAME, got %s", dns.TypeToString[ans[0].Header().Rrtype])
	}
	if ans[0].Header().Name != "anything.example.com." {
		t.Fatalf("owner want anything.example.com. got %s", ans[0].Header().Name)
	}
	if rule != "wildcard:cname" {
		t.Fatalf("rule want wildcard:cname got %s", rule)
	}
}

func TestLookup_Wildcard_ZoneApex_NoMatch(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "192.0.2.99"}}})

	q := dns.Question{Name: "example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	_, _, _, err := s.lookup(new(dns.Msg), q, netip.Addr{})
	if err == nil {
		t.Fatalf("zone apex should not match wildcard")
	}
}

func TestLookup_Wildcard_GeoDNS(t *testing.T) {
	s, db := newTestServer(t)
	z := dbm.Zone{Name: "example.com"}
	db.Create(&z)
	us := "US"
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "*.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{
		{Data: "10.0.0.1", Country: &us},
		{Data: "10.0.0.2"},
	}})

	// With country info -> should select country record
	s.geo = &mockGeo{info: geoip.Info{Country: "US"}}
	q := dns.Question{Name: "test.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	ans, _, rule, err := s.lookup(new(dns.Msg), q, netip.MustParseAddr("203.0.113.1"))
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if a, ok := ans[0].(*dns.A); !ok || a.A.String() != "10.0.0.1" {
		t.Fatalf("want geo 10.0.0.1, got %v", ans[0])
	}
	if rule != "wildcard:country" {
		t.Fatalf("rule want wildcard:country got %s", rule)
	}
}

type mockGeo struct{ info geoip.Info }

func (m *mockGeo) Lookup(netip.Addr) geoip.Info { return m.info }

func TestLookup_CNAME_Fallback(t *testing.T) {
	// Setup in-memory DB and server
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&dbm.Zone{}, &dbm.RRSet{}, &dbm.RData{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{Listen: ":0", RESTListen: ":0", Performance: config.PerformanceConfig{CacheSize: 0, ForwarderTimeoutSec: 1}, GeoIP: config.GeoIPConfig{Enabled: false}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	// Create zone and CNAME at foo.example.com.
	z := dbm.Zone{Name: "example.com"}
	if err := db.Create(&z).Error; err != nil {
		t.Fatalf("create zone: %v", err)
	}
	cname := dbm.RRSet{ZoneID: z.ID, Name: "foo.example.com.", Type: "CNAME", TTL: 300, Records: []dbm.RData{{Data: "bar.example.net."}}}
	if err := db.Create(&cname).Error; err != nil {
		t.Fatalf("create cname: %v", err)
	}

	// Query A foo.example.com. should return CNAME rrset
	q := dns.Question{Name: "foo.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	msg := new(dns.Msg)
	ans, ttl, _, err := s.lookup(msg, q, netip.Addr{})
	if err != nil {
		t.Fatalf("lookup err: %v", err)
	}
	if ttl != 300 {
		t.Fatalf("ttl want 300 got %d", ttl)
	}
	if len(ans) == 0 {
		t.Fatalf("no answers")
	}
	if ans[0].Header().Rrtype != dns.TypeCNAME {
		t.Fatalf("want CNAME got %s", dns.TypeToString[ans[0].Header().Rrtype])
	}
}

func TestInvalidateZoneCache_ClearsAnswerCache(t *testing.T) {
	s, _ := newTestServer(t)
	s.cache = cache.New(10)
	s.cache.Set("www.example.com.|1|", new(dns.Msg), time.Minute)
	s.InvalidateZoneCache()
	if _, ok := s.cache.Get("www.example.com.|1|"); ok {
		t.Fatalf("answer cache must be empty after InvalidateZoneCache")
	}
}
