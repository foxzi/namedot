package dns

import (
	"net"
	"testing"

	"github.com/miekg/dns"

	"namedot/internal/cache"
	dbm "namedot/internal/db"
	"namedot/internal/geoip"
)

func newECSServer(t *testing.T) *Server {
	t.Helper()
	s, db := newTestServer(t)
	s.cfg.GeoIP.UseECS = true
	s.cache = cache.New(100)
	s.geo = &mockGeo{info: geoip.Info{Country: "US"}}
	z := dbm.Zone{Name: "example.com."}
	db.Create(&z)
	us := "US"
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "geo.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{
		{Data: "10.0.0.1", Country: &us},
		{Data: "10.0.0.2"},
	}})
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "plain.example.com.", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "10.0.0.3"}}})
	db.Create(&dbm.RRSet{ZoneID: z.ID, Name: "example.com.", Type: "SOA", TTL: 300, Records: []dbm.RData{{Data: "ns1.example.com. admin.example.com. 1 7200 3600 1209600 300"}}})
	return s
}

func ecsQuery(name string, ip string, prefix uint8) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeA)
	m.SetEdns0(1232, false)
	if ip != "" {
		e := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: prefix, Address: net.ParseIP(ip).To4()}
		opt := m.IsEdns0()
		opt.Option = append(opt.Option, e)
	}
	return m
}

func serve(s *Server, r *dns.Msg) *dns.Msg {
	cw := &cacheWriter{}
	s.serveDNS(cw, r)
	return cw.msg
}

func respECS(t *testing.T, m *dns.Msg) *dns.EDNS0_SUBNET {
	t.Helper()
	opt := m.IsEdns0()
	if opt == nil {
		t.Fatalf("response has no OPT: %v", m)
	}
	for _, o := range opt.Option {
		if e, ok := o.(*dns.EDNS0_SUBNET); ok {
			return e
		}
	}
	return nil
}

func TestECS_ScopeInResponse(t *testing.T) {
	s := newECSServer(t)
	cases := []struct {
		name      string
		rcode     int
		wantScope uint8
	}{
		{"geo.example.com.", dns.RcodeSuccess, 24},
		{"plain.example.com.", dns.RcodeSuccess, 0},
		{"missing.example.com.", dns.RcodeNameError, 0},
	}
	for _, c := range cases {
		// Second pass is served from the cache and must keep the same scope.
		for pass := 0; pass < 2; pass++ {
			r := serve(s, ecsQuery(c.name, "198.51.100.0", 24))
			if r.Rcode != c.rcode {
				t.Fatalf("%s pass %d: rcode %d, want %d", c.name, pass, r.Rcode, c.rcode)
			}
			e := respECS(t, r)
			if e == nil {
				t.Fatalf("%s pass %d: no ECS in response", c.name, pass)
			}
			if e.SourceScope != c.wantScope || e.SourceNetmask != 24 || !e.Address.Equal(net.ParseIP("198.51.100.0")) {
				t.Fatalf("%s pass %d: ECS %v, want scope %d", c.name, pass, e, c.wantScope)
			}
		}
	}
}

func TestECS_ZeroSourcePrefix(t *testing.T) {
	s := newECSServer(t)
	e := respECS(t, serve(s, ecsQuery("geo.example.com.", "0.0.0.0", 0)))
	if e == nil || e.SourceScope != 0 {
		t.Fatalf("want ECS with scope 0, got %v", e)
	}
}

func TestECS_OPTOnlyWithEDNS(t *testing.T) {
	s := newECSServer(t)
	plain := new(dns.Msg)
	plain.SetQuestion("geo.example.com.", dns.TypeA)
	if r := serve(s, plain); r.IsEdns0() != nil {
		t.Fatalf("OPT in response to a query without EDNS")
	}
	r := serve(s, ecsQuery("geo.example.com.", "", 0))
	if r.IsEdns0() == nil || respECS(t, r) != nil {
		t.Fatalf("want OPT without ECS, got %v", r.Extra)
	}
}

func TestECS_Malformed(t *testing.T) {
	s := newECSServer(t)
	// Address bits set beyond the source prefix.
	if r := serve(s, ecsQuery("geo.example.com.", "198.51.100.7", 24)); r.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode %d, want FORMERR", r.Rcode)
	}
	// Unknown family.
	m := ecsQuery("geo.example.com.", "198.51.100.0", 24)
	m.IsEdns0().Option[0].(*dns.EDNS0_SUBNET).Family = 3
	if r := serve(s, m); r.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode %d, want FORMERR", r.Rcode)
	}
}

func TestECS_Disabled(t *testing.T) {
	s := newECSServer(t)
	s.cfg.GeoIP.UseECS = false
	r := serve(s, ecsQuery("geo.example.com.", "198.51.100.7", 24))
	if r.Rcode != dns.RcodeSuccess || respECS(t, r) != nil {
		t.Fatalf("ECS must be ignored when disabled: %v", r)
	}
}
