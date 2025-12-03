package dns

import (
    "context"
    "fmt"
    "log"
    "net"
    "net/netip"
    "strings"
    "time"

    "github.com/miekg/dns"
    "gorm.io/gorm"

    "namedot/internal/cache"
    "namedot/internal/config"
    dbm "namedot/internal/db"
    "namedot/internal/geoip"
)

type Server struct {
    cfg       *config.Config
    db        *gorm.DB
    udpServer *dns.Server
    tcpServer *dns.Server
    resolver  *dns.Client
    cache     *cache.Cache
    zoneCache *ZoneCache
    geo       geoip.Provider
    geoStop   func()
    lastRule  string
}

func NewServer(cfg *config.Config, db *gorm.DB) (*Server, error) {
    s := &Server{
        cfg:       cfg,
        db:        db,
        resolver:  &dns.Client{Timeout: time.Duration(cfg.Performance.ForwarderTimeoutSec) * time.Second},
        cache:     cache.New(cfg.Performance.CacheSize),
        zoneCache: NewZoneCache(5 * time.Minute),
    }
    // GeoIP provider
    if cfg.GeoIP.Enabled && cfg.GeoIP.MMDBPath != "" {
        prov, stop, err := geoip.NewFromPath(
            cfg.GeoIP.MMDBPath,
            time.Duration(cfg.GeoIP.ReloadSec)*time.Second,
            cfg.GeoIP.DownloadURLs,
            time.Duration(cfg.GeoIP.DownloadIntervalSec)*time.Second,
        )
        if err != nil {
            log.Printf("GeoIP: %v; disabling GeoDNS", err)
            s.geo = geoip.NewNoop()
        } else {
            s.geo = prov
            s.geoStop = stop
        }
    } else {
        s.geo = geoip.NewNoop()
    }
    return s, nil
}

func (s *Server) Start() error {
    dns.HandleFunc(".", s.serveDNS)
    s.udpServer = &dns.Server{Addr: s.cfg.Listen, Net: "udp"}
    s.tcpServer = &dns.Server{Addr: s.cfg.Listen, Net: "tcp"}

    go func() {
        if err := s.udpServer.ListenAndServe(); err != nil {
            log.Fatalf("failed to start UDP server: %v", err)
        }
    }()
    go func() {
        if err := s.tcpServer.ListenAndServe(); err != nil {
            log.Fatalf("failed to start TCP server: %v", err)
        }
    }()
    return nil
}

func (s *Server) Shutdown() error {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if s.udpServer != nil {
        _ = s.udpServer.ShutdownContext(ctx)
    }
    if s.tcpServer != nil {
        _ = s.tcpServer.ShutdownContext(ctx)
    }
    if s.geoStop != nil {
        s.geoStop()
    }
    return nil
}

// InvalidateZoneCache clears the zone cache, forcing a refresh on next DNS query
func (s *Server) InvalidateZoneCache() {
    if s.zoneCache != nil {
        s.zoneCache.Invalidate()
    }
}

func (s *Server) serveDNS(w dns.ResponseWriter, r *dns.Msg) {
    m := new(dns.Msg)
    m.SetReply(r)
    m.Authoritative = true

    if len(r.Question) == 0 {
        _ = w.WriteMsg(m)
        return
    }
    q := r.Question[0]
    // Normalize domain name to lowercase (RFC 1123: DNS names are case-insensitive)
    // This prevents cache evasion via case variations (e.g., Example.COM vs example.com)
    q.Name = strings.ToLower(q.Name)
    // Determine client IP (ECS or remote) for geo and cache scoping
    useECS := false
    if s.cfg != nil {
        useECS = s.cfg.GeoIP.UseECS
    }
    cip := clientIPFrom(r, w, useECS)
    prov := s.geo
    if prov == nil {
        prov = geoip.NewNoop()
    }
    ginfo := prov.Lookup(cip)
    verbose := false
    if s.cfg != nil {
        verbose = s.cfg.Log.DNSVerbose
    }
    geoStr := ""
    if verbose {
        geoStr = fmt.Sprintf(" geo[c=%s,ct=%s,asn=%d]", ginfo.Country, ginfo.Continent, ginfo.ASN)
    }

    // Cache key
    cacheScope := cip.String()
    if !cip.IsValid() { cacheScope = "" }
    key := fmt.Sprintf("%s|%d|%s", strings.ToLower(q.Name), q.Qtype, cacheScope)
    if v, ok := s.cache.Get(key); ok {
        if cached, ok2 := v.(*dns.Msg); ok2 {
            log.Printf("DNS QUERY cache-hit q=%s type=%s from=%s%s id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), geoStr, r.Id)
            resp := cached.Copy()
            // Update transaction ID and question to match current request
            resp.Id = r.Id
            resp.Question = r.Question
            _ = w.WriteMsg(resp)
            return
        }
    }

    // Resolve locally
    answers, ttl, err := s.lookup(r, q, cip)
    if err == nil && len(answers) > 0 {
        if verbose {
            log.Printf("DNS QUERY q=%s type=%s from=%s ecs=%s%s rule=%s answers=%d ttl=%d id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), cip, geoStr, s.lastRule, len(answers), ttl, r.Id)
        } else {
            log.Printf("DNS QUERY q=%s type=%s from=%s answers=%d ttl=%d id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), len(answers), ttl, r.Id)
        }
        m.Answer = answers
        _ = w.WriteMsg(m)
        if ttl > 0 {
            // Store a copy in cache to avoid mutating original
            s.cache.Set(key, m.Copy(), time.Duration(ttl)*time.Second)
        }
        return
    }

    // Forward on miss
    if s.cfg.Forwarder != "" {
        fwd := new(dns.Msg)
        fwd.SetQuestion(dns.Fqdn(q.Name), q.Qtype)
        in, _, ferr := s.resolver.Exchange(fwd, net.JoinHostPort(s.cfg.Forwarder, "53"))
        if ferr == nil && in != nil {
            log.Printf("DNS QUERY forward q=%s type=%s from=%s to=%s%s rcode=%d id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), s.cfg.Forwarder, geoStr, in.Rcode, r.Id)
            in.Id = r.Id
            _ = w.WriteMsg(in)
            // Cache negative responses (NXDOMAIN, NODATA, etc.) to prevent repeated upstream queries
            // Use a shorter TTL for negative caching (300 seconds = 5 minutes)
            if in.Rcode != dns.RcodeSuccess {
                s.cache.Set(key, in.Copy(), 5*time.Minute)
            }
            return
        }
    }

    log.Printf("DNS QUERY nxdomain q=%s type=%s from=%s%s id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), geoStr, r.Id)
    m.Rcode = dns.RcodeNameError
    _ = w.WriteMsg(m)
    // Cache local negative responses (no zone found) with short TTL to prevent repeated lookups
    s.cache.Set(key, m.Copy(), 5*time.Minute)
}

// lookup resolves a question from DB applying Geo selection.
func (s *Server) lookup(r *dns.Msg, q dns.Question, clientIP netip.Addr) (answers []dns.RR, ttl uint32, err error) {
    qname := strings.ToLower(dns.Fqdn(q.Name))
    qtype := dns.TypeToString[q.Qtype]

    // Find the best matching zone suffix (using cache)
    zones := s.zoneCache.Get()
    if zones == nil {
        // Cache miss or expired, fetch from database
        if err := s.db.Order("length(name) desc").Find(&zones).Error; err != nil {
            return nil, 0, err
        }
        // Store in cache for future use
        s.zoneCache.Set(zones)
    }
    var zone *dbm.Zone
    for i := range zones {
        name := dns.Fqdn(strings.ToLower(zones[i].Name))
        if strings.HasSuffix(qname, name) {
            zone = &zones[i]
            break
        }
    }
    if zone == nil {
        return nil, 0, fmt.Errorf("no zone")
    }

    // Find RRSet by FQDN name and type
    var set dbm.RRSet
    err = s.db.Preload("Records").
        Where("zone_id = ? AND name = ? AND type = ?", zone.ID, strings.ToLower(qname), strings.ToUpper(qtype)).
        First(&set).Error
    if err != nil {
        // If exact type not found, try CNAME fallback for this name
        var cnameSet dbm.RRSet
        if e2 := s.db.Preload("Records").
            Where("zone_id = ? AND name = ? AND type = ?", zone.ID, strings.ToLower(qname), "CNAME").
            First(&cnameSet).Error; e2 == nil {
            // Return CNAME rrset as the answer; resolvers will chase it
            for _, rec := range cnameSet.Records {
                // Support "@" shorthand in CNAME target to mean zone apex
                target := rec.Data
                if strings.TrimSpace(target) == "@" {
                    target = dns.Fqdn(strings.ToLower(zone.Name))
                }
                rr, perr := dns.NewRR(fmt.Sprintf("%s %d CNAME %s", qname, cnameSet.TTL, target))
                if perr == nil { answers = append(answers, rr) }
            }
            return answers, cnameSet.TTL, nil
        }
        return nil, 0, err
    }

    // Geo selection
    g := s.geo.Lookup(clientIP)
    recs, rule := selectGeoRecords(set.Records, clientIP, g)
    s.lastRule = rule

    for _, rec := range recs {
        // If answering CNAME directly, support "@" shorthand for apex in target
        data := rec.Data
        if strings.EqualFold(qtype, "CNAME") && strings.TrimSpace(data) == "@" {
            data = dns.Fqdn(strings.ToLower(zone.Name))
        }
        rr, perr := dns.NewRR(fmt.Sprintf("%s %d %s %s", qname, set.TTL, strings.ToUpper(qtype), data))
        if perr == nil {
            answers = append(answers, rr)
        }
    }
    return answers, set.TTL, nil
}

func clientIPFrom(r *dns.Msg, w dns.ResponseWriter, useECS bool) netip.Addr {
    if useECS {
        if opt := r.IsEdns0(); opt != nil {
            for _, o := range opt.Option {
                if ecs, ok := o.(*dns.EDNS0_SUBNET); ok {
                    var ip net.IP
                    if ecs.Family == 1 { // IPv4
                        ip = ecs.Address.To4()
                    } else {
                        ip = ecs.Address
                    }
                    if ip != nil {
                        a, _ := netip.ParseAddr(ip.String())
                        return a
                    }
                }
            }
        }
    }
    if ra := w.RemoteAddr(); ra != nil {
        host, _, err := net.SplitHostPort(ra.String())
        if err == nil {
            if a, err2 := netip.ParseAddr(host); err2 == nil { return a }
        }
    }
    return netip.Addr{}
}

// remapIP maps an IP from one CIDR into another CIDR with the same prefix length.
// Useful to translate reserved ranges (e.g., 127.0.1.0/24) into TEST-NET for GeoIP lookup.

func selectGeoRecords(recs []dbm.RData, ip netip.Addr, g geoip.Info) ([]dbm.RData, string) {
    if len(recs) == 0 {
        return recs, "none"
    }
    // If no IP, return generic ones or all
    if !ip.IsValid() {
        out := make([]dbm.RData, 0, len(recs))
        for _, r := range recs {
            if r.Country == nil && r.Continent == nil && r.ASN == nil && r.Subnet == nil {
                out = append(out, r)
            }
        }
        if len(out) > 0 {
            return out, "generic"
        }
        return recs, "all"
    }
    // Priority: subnet > asn > country > continent > default
    var subnetMatch, asnMatch, countryMatch, continentMatch, generic []dbm.RData
    for _, r := range recs {
        if r.Subnet != nil {
            if p, err := netip.ParsePrefix(*r.Subnet); err == nil && p.Contains(ip) {
                subnetMatch = append(subnetMatch, r)
                continue
            }
        }
        if r.ASN != nil {
            if g.ASN != 0 && *r.ASN == g.ASN {
                asnMatch = append(asnMatch, r)
                continue
            }
        }
        if r.Country != nil && g.Country != "" && strings.EqualFold(*r.Country, g.Country) {
            countryMatch = append(countryMatch, r)
            continue
        }
        if r.Continent != nil && g.Continent != "" && strings.EqualFold(*r.Continent, g.Continent) {
            continentMatch = append(continentMatch, r)
            continue
        }
        if r.Country == nil && r.Continent == nil && r.ASN == nil && r.Subnet == nil {
            generic = append(generic, r)
        }
    }
    if len(subnetMatch) > 0 {
        return subnetMatch, "subnet"
    }
    if len(asnMatch) > 0 {
        return asnMatch, "asn"
    }
    if len(countryMatch) > 0 {
        return countryMatch, "country"
    }
    if len(continentMatch) > 0 {
        return continentMatch, "continent"
    }
    if len(generic) > 0 {
        return generic, "generic"
    }
    return recs, "all"
}
