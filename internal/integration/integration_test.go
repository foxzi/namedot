package integration

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net"
    "net/http"
    "testing"
    "time"

    "github.com/miekg/dns"
    "strconv"
    "gorm.io/gorm"
    "path/filepath"

    "namedot/internal/config"
    "namedot/internal/db"
    dnssrv "namedot/internal/server/dns"
    restsrv "namedot/internal/server/rest"
)

func waitHTTPReady(url string, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        resp, err := http.Get(url)
        if err == nil {
            resp.Body.Close()
            return nil
        }
        time.Sleep(100 * time.Millisecond)
    }
    return &net.AddrError{Err: "timeout", Addr: url}
}

func TestEndToEnd_DNS_and_REST(t *testing.T) {
    // Ports for test
    dnsAddr := "127.0.0.1:19053"
    restAddr := "127.0.0.1:18089"

    tmpDB := filepath.Join(t.TempDir(), "integration_e2e.db")
    cfg := &config.Config{
        Listen:           dnsAddr,
        Forwarder:        "",
        EnableDNSSEC:     false,
        APIToken:         "devtoken",
        RESTListen:       restAddr,
        DefaultTTL:       300,
        SOA: config.SOAConfig{AutoOnMissing: true},
        DB: config.DBConfig{
            Driver: "sqlite",
            DSN:    fmt.Sprintf("file:%s?_foreign_keys=on", tmpDB),
        },
        GeoIP: config.GeoIPConfig{Enabled: false},
    }

    // DB
    gormDB, err := db.OpenWithDebug(cfg.DB, false)
    if err != nil { t.Fatalf("open db: %v", err) }
    if err := db.AutoMigrate(gormDB); err != nil { t.Fatalf("migrate: %v", err) }

    // Servers
    dnsServer, err := dnssrv.NewServer(cfg, gormDB)
    if err != nil { t.Fatalf("dns: %v", err) }
    restServer := restsrv.NewServer(cfg, gormDB, dnsServer)

    go func() { _ = dnsServer.Start() }()
    go func() { _ = restServer.Start() }()

    // Wait for REST to be ready
    if err := waitHTTPReady("http://"+restAddr+"/zones", 5*time.Second); err != nil {
        t.Fatalf("rest not ready: %v", err)
    }

    // REST: create zone
    type zoneResp struct{ ID uint `json:"id"` }
    zr := zoneResp{}
    reqBody := bytes.NewBufferString(`{"name":"example.int"}`)
    req, _ := http.NewRequest("POST", "http://"+restAddr+"/zones", reqBody)
    req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil { t.Fatalf("create zone: %v", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusCreated { t.Fatalf("create zone status: %d", resp.StatusCode) }
    if err := json.NewDecoder(resp.Body).Decode(&zr); err != nil { t.Fatalf("decode: %v", err) }
    if zr.ID == 0 { t.Fatalf("zone id is 0") }

    // REST: add A rrset
    rrJSON := `{"name":"www","type":"A","ttl":300,"records":[{"data":"192.0.2.55"}]}`
    req2, _ := http.NewRequest("POST", "http://"+restAddr+"/zones/"+itoa(zr.ID)+"/rrsets", bytes.NewBufferString(rrJSON))
    req2.Header.Set("Authorization", "Bearer "+cfg.APIToken)
    req2.Header.Set("Content-Type", "application/json")
    resp2, err := http.DefaultClient.Do(req2)
    if err != nil { t.Fatalf("create rrset: %v", err) }
    resp2.Body.Close()
    if resp2.StatusCode != http.StatusCreated { t.Fatalf("create rrset status: %d", resp2.StatusCode) }

    // DNS: query
    c := &dns.Client{Timeout: 2 * time.Second}
    m := new(dns.Msg)
    m.SetQuestion("www.example.int.", dns.TypeA)
    in, _, err := c.Exchange(m, dnsAddr)
    if err != nil { t.Fatalf("dns exchange: %v", err) }
    if in.Rcode != dns.RcodeSuccess { t.Fatalf("rcode: %d", in.Rcode) }
    if len(in.Answer) == 0 { t.Fatalf("no answers") }

    // Check A record value
    ok := false
    for _, rr := range in.Answer {
        if a, _ := rr.(*dns.A); a != nil && a.A.String() == "192.0.2.55" { ok = true; break }
    }
    if !ok { t.Fatalf("A 192.0.2.55 not found in answers: %#v", in.Answer) }

    // Try again to exercise cache path
    in2, _, err := c.Exchange(m, dnsAddr)
    if err != nil || in2.Rcode != dns.RcodeSuccess { t.Fatalf("dns cache exchange err=%v rcode=%d", err, in2.Rcode) }

    // Shutdown DNS (REST has no graceful shutdown here)
    _ = dnsServer.Shutdown()
    _ = gormDB.Transaction(func(tx *gorm.DB) error { return nil })
}

func itoa(u uint) string { return strconv.FormatUint(uint64(u), 10) }
