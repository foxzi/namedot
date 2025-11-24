package integration

import (
    "bytes"
    "encoding/json"
    "net/netip"
    "net"
    "net/http"
    "os"
    "path/filepath"
    "strconv"
    "testing"
    "time"

    "github.com/miekg/dns"

    "namedot/internal/config"
    "namedot/internal/db"
    "namedot/internal/geoip"
    dnssrv "namedot/internal/server/dns"
    restsrv "namedot/internal/server/rest"
)

// Test GeoDNS selection using ECS with a known US IP (8.8.8.8).
func TestGeoDNS_WithECS_USCountry(t *testing.T) {
    // Locate geoipdb directory from repo root
    // This test runs from package dir internal/integration
    geoDir := filepath.Clean(filepath.Join("..", "..", "geoipdb"))
    if st, err := os.Stat(geoDir); err != nil || !st.IsDir() {
        t.Skipf("geoipdb directory not found; skipping GeoDNS test")
    }
    // Quick check there is at least one .mmdb file
    files, _ := os.ReadDir(geoDir)
    hasMMDB := false
    for _, f := range files { if !f.IsDir() && filepath.Ext(f.Name()) == ".mmdb" { hasMMDB = true; break } }
    if !hasMMDB { t.Skip("no .mmdb files in geoipdb; skipping") }

    dnsAddr := "127.0.0.1:19054"
    restAddr := "127.0.0.1:18090"

    tmpDB := filepath.Join(t.TempDir(), "geo_integration.db")
    cfg := &config.Config{
        Listen:           dnsAddr,
        Forwarder:        "",
        EnableDNSSEC:     false,
        APIToken:         "devtoken",
        RESTListen:       restAddr,
        DefaultTTL:       60,
        SOA:              config.SOAConfig{AutoOnMissing: true},
        DB: config.DBConfig{Driver: "sqlite", DSN: "file:" + tmpDB + "?_foreign_keys=on"},
        GeoIP: config.GeoIPConfig{Enabled: true, MMDBPath: geoDir, ReloadSec: 0, UseECS: true},
    }

    gormDB, err := db.OpenWithDebug(cfg.DB, false)
    if err != nil { t.Fatalf("open db: %v", err) }
    if err := db.AutoMigrate(gormDB); err != nil { t.Fatalf("migrate: %v", err) }

    dnsServer, err := dnssrv.NewServer(cfg, gormDB)
    if err != nil { t.Fatalf("dns: %v", err) }
    restServer := restsrv.NewServer(cfg, gormDB, dnsServer)

    go func() { _ = dnsServer.Start() }()
    go func() { _ = restServer.Start() }()

    if err := waitHTTPReady("http://"+restAddr+"/zones", 5*time.Second); err != nil {
        t.Fatalf("rest not ready: %v", err)
    }

    // Create zone
    type zoneResp struct{ ID uint `json:"id"` }
    zr := zoneResp{}
    reqBody := bytes.NewBufferString(`{"name":"geodns.test"}`)
    req, _ := http.NewRequest("POST", "http://"+restAddr+"/zones", reqBody)
    req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil { t.Fatalf("create zone: %v", err) }
    if resp.StatusCode != http.StatusCreated { t.Fatalf("create zone status: %d", resp.StatusCode) }
    _ = json.NewDecoder(resp.Body).Decode(&zr)
    resp.Body.Close()

    // Add RRset prioritizing subnet match for ECS 8.8.8.8, plus country-specific and generic
    rrJSON := `{"name":"svc","type":"A","ttl":60,"records":[{"data":"198.51.100.11","country":"US"},{"data":"198.51.100.13","subnet":"8.8.8.0/24"},{"data":"198.51.100.12"}]}`
    req2, _ := http.NewRequest("POST", "http://"+restAddr+"/zones/"+itoa(zr.ID)+"/rrsets", bytes.NewBufferString(rrJSON))
    req2.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req2.Header.Set("Content-Type", "application/json")
    resp2, err := http.DefaultClient.Do(req2)
    if err != nil { t.Fatalf("create rrset: %v", err) }
    if resp2.StatusCode != http.StatusCreated { t.Fatalf("create rrset status: %d", resp2.StatusCode) }
    resp2.Body.Close()

    // DNS query with ECS=8.8.8.8/24 (US)
    c := &dns.Client{Timeout: 2 * time.Second}
    m := new(dns.Msg)
    m.SetQuestion("svc.geodns.test.", dns.TypeA)
    // add ECS option
    opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
    ecs := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, SourceScope: 0}
    ecs.Address = net.ParseIP("8.8.8.8")
    opt.Option = append(opt.Option, ecs)
    m.Extra = append(m.Extra, opt)

    in, _, err := c.Exchange(m, dnsAddr)
    if err != nil { t.Fatalf("dns exchange: %v", err) }
    if in.Rcode != dns.RcodeSuccess { t.Fatalf("rcode: %d", in.Rcode) }
    if len(in.Answer) == 0 { t.Fatalf("no answers") }
    // Expect subnet-specific record selected (priority higher than country)
    found := false
    for _, rr := range in.Answer {
        if a, _ := rr.(*dns.A); a != nil && a.A.String() == "198.51.100.13" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("expected US-specific A 198.51.100.11, got: %#v", in.Answer)
    }

    _ = dnsServer.Shutdown()
}

// Try multiple candidate IPs to find one with country/continent/ASN present in the provided MMDBs.
func pickIPFor(t *testing.T, geoDir string) (ip netip.Addr, info geoip.Info, prov geoip.Provider) {
    t.Helper()
    p, _, _ := geoip.NewFromPath(geoDir, 0, nil, 0)
    prov = p
    candidates := []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}
    for _, c := range candidates {
        a := netip.MustParseAddr(c)
        inf := p.Lookup(a)
        if inf.Country != "" || inf.Continent != "" || inf.ASN != 0 {
            return a, inf, p
        }
    }
    t.Skip("no candidate IP resolved in GeoIP DB; skipping")
    return netip.Addr{}, geoip.Info{}, p
}

func TestGeoDNS_WithECS_Country_Continent_ASN(t *testing.T) {
    geoDir := filepath.Clean(filepath.Join("..", "..", "geoipdb"))
    if st, err := os.Stat(geoDir); err != nil || !st.IsDir() { t.Skip("geoipdb not found") }
    entries, _ := os.ReadDir(geoDir)
    hasMMDB := false
    for _, e := range entries { if !e.IsDir() && filepath.Ext(e.Name()) == ".mmdb" { hasMMDB = true; break } }
    if !hasMMDB { t.Skip("no mmdb files") }

    ip, info, _ := pickIPFor(t, geoDir)

    dnsAddr := "127.0.0.1:19055"
    restAddr := "127.0.0.1:18091"
    tmpDB := filepath.Join(t.TempDir(), "geo_multi.db")
    cfg := &config.Config{
        Listen: dnsAddr, RESTListen: restAddr, APIToken: "devtoken",
        DefaultTTL: 60,
        SOA: config.SOAConfig{AutoOnMissing: true},
        DB: config.DBConfig{Driver: "sqlite", DSN: "file:" + tmpDB + "?_foreign_keys=on"},
        GeoIP: config.GeoIPConfig{Enabled: true, MMDBPath: geoDir, ReloadSec: 0, UseECS: true},
    }
    gdb, err := db.OpenWithDebug(cfg.DB, false); if err != nil { t.Fatal(err) }
    if err := db.AutoMigrate(gdb); err != nil { t.Fatal(err) }
    dnsServer, _ := dnssrv.NewServer(cfg, gdb)
    restServer := restsrv.NewServer(cfg, gdb, dnsServer)
    go func() { _ = dnsServer.Start() }()
    go func() { _ = restServer.Start() }()
    if err := waitHTTPReady("http://"+restAddr+"/zones", 5*time.Second); err != nil { t.Fatal(err) }

    // Create zone
    type zoneResp struct{ ID uint `json:"id"` }
    zr := zoneResp{}
    reqBody := bytes.NewBufferString(`{"name":"geo1.test"}`)
    req, _ := http.NewRequest("POST", "http://"+restAddr+"/zones", reqBody)
    req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req); if err != nil { t.Fatal(err) }
    if resp.StatusCode != http.StatusCreated { t.Fatalf("zone status %d", resp.StatusCode) }
    _ = json.NewDecoder(resp.Body).Decode(&zr); resp.Body.Close()

    // Prepare rrset body with available selectors from info
    // Always include generic; include country/continent/asn if known
    recs := `{"data":"203.0.113.1"}`
    if info.Continent != "" { recs = `{"data":"203.0.113.3","continent":"` + info.Continent + `"},` + recs }
    if info.Country != "" { recs = `{"data":"203.0.113.2","country":"` + info.Country + `"},` + recs }
    if info.ASN != 0 { recs = `{"data":"203.0.113.4","asn":` + strconv.Itoa(info.ASN) + `},` + recs }
    body := `{"name":"host","type":"A","ttl":60,"records":[` + recs + `]}`
    req2, _ := http.NewRequest("POST", "http://"+restAddr+"/zones/"+itoa(zr.ID)+"/rrsets", bytes.NewBufferString(body))
    req2.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req2.Header.Set("Content-Type", "application/json")
    resp2, err := http.DefaultClient.Do(req2); if err != nil { t.Fatal(err) }
    if resp2.StatusCode != http.StatusCreated { t.Fatalf("rrset status %d", resp2.StatusCode) }
    resp2.Body.Close()

    // Build ECS for chosen IP
    ecsOpt := func() *dns.OPT {
        opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
        fam := uint16(1); if ip.Is6() { fam = 2 }
        e := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: fam, SourceNetmask: 24}
        if ip.Is6() { e.SourceNetmask = 56 }
        e.Address = net.ParseIP(ip.String())
        opt.Option = append(opt.Option, e)
        return opt
    }

    // DNS query
    c := &dns.Client{Timeout: 2 * time.Second}
    m := new(dns.Msg)
    m.SetQuestion("host.geo1.test.", dns.TypeA)
    m.Extra = append(m.Extra, ecsOpt())
    in, _, err := c.Exchange(m, dnsAddr); if err != nil { t.Fatal(err) }
    if in.Rcode != dns.RcodeSuccess || len(in.Answer) == 0 { t.Fatalf("rcode=%d answers=%d", in.Rcode, len(in.Answer)) }

    // Validate priority: if ASN present expected 203.0.113.4, else if country present -> .2, else if continent present -> .3
    want := "203.0.113.1"
    if info.Continent != "" { want = "203.0.113.3" }
    if info.Country != "" { want = "203.0.113.2" }
    if info.ASN != 0 { want = "203.0.113.4" }
    found := false
    for _, rr := range in.Answer {
        if a, _ := rr.(*dns.A); a != nil && a.A.String() == want { found = true; break }
    }
    if !found { t.Fatalf("expected %s, got %#v", want, in.Answer) }
    _ = dnsServer.Shutdown()
}
