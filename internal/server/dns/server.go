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

// InvalidateZoneCache clears the zone cache and cached answers, forcing a refresh on next DNS query
func (s *Server) InvalidateZoneCache() {
	if s.zoneCache != nil {
		s.zoneCache.Invalidate()
	}
	if s.cache != nil {
		s.cache.Clear()
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
	ecs, ok := requestECS(r, useECS)
	if !ok {
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(withOPT(r, m, nil, false))
		return
	}
	cip := clientIPFrom(w, ecs)
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
	if !cip.IsValid() {
		cacheScope = ""
	}
	key := fmt.Sprintf("%s|%d|%s", strings.ToLower(q.Name), q.Qtype, cacheScope)
	if v, ok := s.cache.Get(key); ok {
		if entry, ok2 := v.(*cacheEntry); ok2 {
			cached := entry.msg
			log.Printf("DNS QUERY cache-hit q=%s type=%s from=%s%s id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), geoStr, r.Id)
			resp := cached.Copy()
			// Update transaction ID and question to match current request
			resp.Id = r.Id
			resp.Question = r.Question
			writeMsg(w, r, withOPT(r, resp, ecs, entry.geo))
			return
		}
	}

	// Resolve locally
	answers, ttl, rule, geo, err := s.lookup(r, q, cip)
	if err == nil && len(answers) > 0 {
		if verbose {
			log.Printf("DNS QUERY q=%s type=%s from=%s ecs=%s%s rule=%s answers=%d ttl=%d id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), cip, geoStr, rule, len(answers), ttl, r.Id)
		} else {
			log.Printf("DNS QUERY q=%s type=%s from=%s answers=%d ttl=%d id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), len(answers), ttl, r.Id)
		}
		m.Answer = answers
		writeMsg(w, r, withOPT(r, m, ecs, geo))
		if ttl > 0 {
			// Store a copy in cache to avoid mutating original
			s.cache.Set(key, &cacheEntry{msg: m.Copy(), geo: geo}, time.Duration(ttl)*time.Second)
		}
		return
	}

	// Names inside our own zones are answered authoritatively (RFC 2308):
	// NXDOMAIN or NODATA with the zone SOA, never forwarded.
	if zone := s.zoneFor(q.Name); zone != nil {
		rcode, soa, negTTL := s.negativeAnswer(zone, strings.ToLower(dns.Fqdn(q.Name)))
		log.Printf("DNS QUERY negative q=%s type=%s from=%s%s rcode=%s id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), geoStr, dns.RcodeToString[rcode], r.Id)
		m.Rcode = rcode
		if soa != nil {
			m.Ns = []dns.RR{soa}
		}
		writeMsg(w, r, withOPT(r, m, ecs, false))
		if negTTL > 0 {
			s.cache.Set(key, &cacheEntry{msg: m.Copy()}, time.Duration(negTTL)*time.Second)
		}
		return
	}

	// Forward names outside our zones
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
				s.cache.Set(key, &cacheEntry{msg: in.Copy()}, 5*time.Minute)
			}
			return
		}
	}

	log.Printf("DNS QUERY nxdomain q=%s type=%s from=%s%s id=%d", q.Name, dns.TypeToString[q.Qtype], w.RemoteAddr(), geoStr, r.Id)
	m.Rcode = dns.RcodeNameError
	_ = w.WriteMsg(withOPT(r, m, ecs, false))
	// Cache local negative responses (no zone found) with short TTL to prevent repeated lookups
	s.cache.Set(key, &cacheEntry{msg: m.Copy()}, 5*time.Minute)
}

// zoneFor returns the zone that contains qname, or nil.
func (s *Server) zoneFor(qname string) *dbm.Zone {
	zones, err := s.loadZones()
	if err != nil {
		return nil
	}
	return findZone(zones, strings.ToLower(dns.Fqdn(qname)))
}

// negativeAnswer decides between NXDOMAIN and NODATA for a name of zone that has
// no answer, and returns the zone SOA for the authority section together with the
// negative caching TTL: min(SOA TTL, SOA minimum).
func (s *Server) negativeAnswer(zone *dbm.Zone, qname string) (int, dns.RR, uint32) {
	rcode := dns.RcodeNameError
	// The name exists if it owns records, is an empty non-terminal or matches a wildcard.
	names := append([]string{qname}, wildcardNames(qname, dns.Fqdn(strings.ToLower(zone.Name)))...)
	var n int64
	s.db.Model(&dbm.RRSet{}).
		Where("zone_id = ? AND (name IN ? OR name LIKE ? ESCAPE '!')", zone.ID, names, "%."+escapeLike(qname)).
		Count(&n)
	if n > 0 {
		rcode = dns.RcodeSuccess
	}

	var set dbm.RRSet
	if err := s.db.Preload("Records").Where("zone_id = ? AND type = ?", zone.ID, "SOA").First(&set).Error; err != nil || len(set.Records) == 0 {
		return rcode, nil, 300
	}
	rr, err := dns.NewRR(fmt.Sprintf("%s %d SOA %s", dns.Fqdn(set.Name), set.TTL, set.Records[0].Data))
	soa, ok := rr.(*dns.SOA)
	if err != nil || !ok {
		return rcode, nil, 300
	}
	ttl := min(soa.Hdr.Ttl, soa.Minttl)
	soa.Hdr.Ttl = ttl
	return rcode, soa, ttl
}

func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// cacheEntry is a cached answer; geo marks answers that depend on the client address.
type cacheEntry struct {
	msg *dns.Msg
	geo bool
}

// ednsUDPSize is the UDP payload size advertised in responses (DNS flag day 2020).
const ednsUDPSize = 1232

// withOPT returns m with an OPT record when the request carried EDNS0 (RFC 6891).
// A request ECS option is echoed back (RFC 7871): SCOPE equals SOURCE PREFIX-LENGTH
// for geo-dependent answers and 0 for answers valid for any client.
// m itself is left intact because it may be stored in the cache.
func withOPT(r, m *dns.Msg, ecs *dns.EDNS0_SUBNET, geo bool) *dns.Msg {
	if r.IsEdns0() == nil {
		return m
	}
	m = m.Copy()
	opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
	opt.SetUDPSize(ednsUDPSize)
	if ecs != nil {
		e := *ecs
		e.SourceScope = 0
		if geo {
			e.SourceScope = e.SourceNetmask
		}
		opt.Option = append(opt.Option, &e)
	}
	m.Extra = append(m.Extra, opt)
	return m
}

// writeMsg sends m, truncating UDP answers to the client buffer size (512 bytes
// without EDNS0) so the client sees the TC flag and retries over TCP.
// m itself is left intact because it may be stored in the cache.
func writeMsg(w dns.ResponseWriter, r, m *dns.Msg) {
	if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
		size := dns.MinMsgSize
		if opt := r.IsEdns0(); opt != nil {
			size = int(opt.UDPSize())
		}
		if m.Len() > size {
			m = m.Copy()
			m.Truncate(size)
		}
	}
	_ = w.WriteMsg(m)
}

// lookup resolves a question from DB applying Geo selection.
// geo reports whether the answer depends on the client address.
func (s *Server) lookup(r *dns.Msg, q dns.Question, clientIP netip.Addr) (answers []dns.RR, ttl uint32, rule string, geo bool, err error) {
	qname := strings.ToLower(dns.Fqdn(q.Name))
	qtype := dns.TypeToString[q.Qtype]

	// Find the best matching zone suffix (using cache)
	zones, err := s.loadZones()
	if err != nil {
		return nil, 0, "", false, err
	}
	zone := findZone(zones, qname)
	if zone == nil {
		return nil, 0, "", false, fmt.Errorf("no zone")
	}

	zoneFQDN := dns.Fqdn(strings.ToLower(zone.Name))

	// Find RRSet by FQDN name and type
	if answers, ttl, rule, geo, err = s.lookupExact(qname, qtype, zone, clientIP); err == nil {
		return answers, ttl, rule, geo, nil
	}

	// Wildcard fallback (RFC 4592)
	for _, wcName := range wildcardNames(qname, zoneFQDN) {
		if answers, ttl, rule, geo, err = s.lookupExact(wcName, qtype, zone, clientIP); err == nil {
			// Rewrite owner name from wildcard to original qname
			for i, rr := range answers {
				rr.Header().Name = qname
				answers[i] = rr
			}
			return answers, ttl, "wildcard:" + rule, geo, nil
		}
	}

	return nil, 0, "", false, fmt.Errorf("not found")
}

// loadZones returns all zones sorted by name length desc, using the zone cache.
func (s *Server) loadZones() ([]dbm.Zone, error) {
	zones := s.zoneCache.Get()
	if zones == nil {
		// Cache miss or expired, fetch from database
		if err := s.db.Order("length(name) desc").Find(&zones).Error; err != nil {
			return nil, err
		}
		s.zoneCache.Set(zones)
	}
	return zones, nil
}

// findZone returns the longest zone containing qname; zones must be sorted by name length desc.
// Matching is done on label boundaries, so zone "ample.com." does not contain "example.com.".
func findZone(zones []dbm.Zone, qname string) *dbm.Zone {
	for i := range zones {
		if dns.IsSubDomain(dns.Fqdn(strings.ToLower(zones[i].Name)), qname) {
			return &zones[i]
		}
	}
	return nil
}

// lookupExact tries exact name+type match, then CNAME fallback for the given name.
func (s *Server) lookupExact(qname, qtype string, zone *dbm.Zone, clientIP netip.Addr) ([]dns.RR, uint32, string, bool, error) {
	zoneFQDN := dns.Fqdn(strings.ToLower(zone.Name))

	var set dbm.RRSet
	err := s.db.Preload("Records").
		Where("zone_id = ? AND name = ? AND type = ?", zone.ID, strings.ToLower(qname), strings.ToUpper(qtype)).
		First(&set).Error
	if err != nil {
		// If exact type not found, try CNAME fallback for this name
		var cnameSet dbm.RRSet
		if e2 := s.db.Preload("Records").
			Where("zone_id = ? AND name = ? AND type = ?", zone.ID, strings.ToLower(qname), "CNAME").
			First(&cnameSet).Error; e2 == nil {
			var answers []dns.RR
			for _, rec := range cnameSet.Records {
				target := rec.Data
				if strings.TrimSpace(target) == "@" {
					target = zoneFQDN
				}
				rr, perr := dns.NewRR(fmt.Sprintf("%s %d CNAME %s", qname, cnameSet.TTL, target))
				if perr == nil {
					answers = append(answers, rr)
				}
			}
			return answers, cnameSet.TTL, "cname", false, nil
		}
		return nil, 0, "", false, err
	}

	// Geo selection
	g := s.geo.Lookup(clientIP)
	recs, selectedRule := selectGeoRecords(set.Records, clientIP, g)

	var answers []dns.RR
	for _, rec := range recs {
		data := rec.Data
		if strings.EqualFold(qtype, "CNAME") && strings.TrimSpace(data) == "@" {
			data = zoneFQDN
		}
		rr, perr := dns.NewRR(fmt.Sprintf("%s %d %s %s", qname, set.TTL, strings.ToUpper(qtype), data))
		if perr == nil {
			answers = append(answers, rr)
		}
	}
	return answers, set.TTL, selectedRule, hasGeoRecords(set.Records), nil
}

// hasGeoRecords reports whether any record carries a geo condition.
func hasGeoRecords(recs []dbm.RData) bool {
	for _, r := range recs {
		if r.Country != nil || r.Continent != nil || r.ASN != nil || r.Subnet != nil {
			return true
		}
	}
	return false
}

// wildcardNames returns wildcard candidate names for a query, from most specific to least.
// E.g. for qname "foo.bar.example.com." and zone "example.com." it returns:
// ["*.bar.example.com.", "*.example.com."]
func wildcardNames(qname, zoneFQDN string) []string {
	// Zone apex cannot match a wildcard
	if qname == zoneFQDN {
		return nil
	}
	var candidates []string
	// Strip leftmost label iteratively
	rest := qname
	for {
		idx := strings.Index(rest, ".")
		if idx < 0 {
			break
		}
		rest = rest[idx+1:]
		if rest == "" {
			break
		}
		candidate := "*." + rest
		candidates = append(candidates, candidate)
		// Stop when we've reached the zone level
		if rest == zoneFQDN {
			break
		}
	}
	return candidates
}

// requestECS returns the ECS option of the request when ECS is enabled, or nil.
// ok is false for a malformed option (RFC 7871 section 7.1.1): unknown family,
// prefix longer than the address or address bits set beyond the prefix.
func requestECS(r *dns.Msg, useECS bool) (ecs *dns.EDNS0_SUBNET, ok bool) {
	opt := r.IsEdns0()
	if !useECS || opt == nil {
		return nil, true
	}
	for _, o := range opt.Option {
		e, isECS := o.(*dns.EDNS0_SUBNET)
		if !isECS {
			continue
		}
		var addr netip.Addr
		switch e.Family {
		case 1:
			addr, ok = netip.AddrFromSlice(e.Address.To4())
		case 2:
			addr, ok = netip.AddrFromSlice(e.Address.To16())
		}
		if !ok || int(e.SourceNetmask) > addr.BitLen() {
			return nil, false
		}
		if netip.PrefixFrom(addr, int(e.SourceNetmask)).Masked().Addr() != addr {
			return nil, false
		}
		return e, true
	}
	return nil, true
}

// clientIPFrom returns the ECS address, or the remote address when there is no
// ECS or its SOURCE PREFIX-LENGTH is 0 (the client asked not to use its address).
func clientIPFrom(w dns.ResponseWriter, ecs *dns.EDNS0_SUBNET) netip.Addr {
	if ecs != nil && ecs.SourceNetmask > 0 {
		if a, ok := netip.AddrFromSlice(ecs.Address); ok {
			return a.Unmap()
		}
	}
	if ra := w.RemoteAddr(); ra != nil {
		host, _, err := net.SplitHostPort(ra.String())
		if err == nil {
			if a, err2 := netip.ParseAddr(host); err2 == nil {
				return a
			}
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
