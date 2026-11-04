package geoip

import (
    "errors"
    "fmt"
    "io"
    "log"
    "net/http"
    "net/netip"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "time"

    geoip2 "github.com/oschwald/geoip2-golang"
    "github.com/oschwald/maxminddb-golang"
)

type Info struct {
    Country   string
    Continent string
    ASN       int
}

type Provider interface {
    Lookup(ip netip.Addr) Info
}

type noop struct{}

func (noop) Lookup(ip netip.Addr) Info { return Info{} }

func NewNoop() Provider { return noop{} }

// dbReader wraps both geoip2 and maxminddb readers
type dbReader struct {
    geoip2Reader *geoip2.Reader
    rawReader    *maxminddb.Reader
    dbType       string // "city", "asn", etc
}

func (r *dbReader) Close() error {
    if r.geoip2Reader != nil {
        return r.geoip2Reader.Close()
    }
    if r.rawReader != nil {
        return r.rawReader.Close()
    }
    return nil
}

// continentFromCountry returns continent code from country code (ISO 3166-1 alpha-2)
func continentFromCountry(countryCode string) string {
    // Map of country code to continent code
    // Using a simple map for common countries - this is not exhaustive
    continents := map[string]string{
        "US": "NA", "CA": "NA", "MX": "NA",
        "GB": "EU", "FR": "EU", "DE": "EU", "IT": "EU", "ES": "EU", "NL": "EU", "RU": "EU",
        "CN": "AS", "JP": "AS", "IN": "AS", "KR": "AS", "SG": "AS",
        "BR": "SA", "AR": "SA", "CL": "SA",
        "AU": "OC", "NZ": "OC",
        "ZA": "AF", "EG": "AF", "NG": "AF",
    }
    if continent, ok := continents[countryCode]; ok {
        return continent
    }
    // Fallback: try to guess from first letter of country code
    if len(countryCode) < 2 {
        return ""
    }
    switch countryCode[:1] {
    case "A":
        if countryCode >= "AF" && countryCode <= "AZ" {
            return "AS" // Asia
        }
        return "AF" // Africa
    case "B", "C":
        return "SA" // South America
    case "D", "E", "F", "G", "H", "I", "L", "M", "N", "P", "R", "S", "T", "U", "V":
        return "EU" // Europe
    }
    return ""
}

// readerSet holds the databases loaded by one load pass.
// The same reader may occupy several slots.
type readerSet struct {
    country4, country6, asn4, asn6 *dbReader
}

func (s readerSet) close() {
    seen := map[*dbReader]bool{}
    for _, r := range []*dbReader{s.country4, s.country6, s.asn4, s.asn6} {
        if r != nil && !seen[r] {
            seen[r] = true
            _ = r.Close()
        }
    }
}

// MaxMind provider that can load Country/ASN DBs for IPv4 and IPv6, with hot-reload.
// Lookups hold mu.RLock, so old readers are closed only after no lookup uses them.
type maxmind struct {
    path string // file or directory

    mu     sync.RWMutex
    set    readerSet
    loadMu sync.Mutex // serializes reloads from the reload and download goroutines
}

// NewFromPath loads GeoIP databases. If path is a directory, loads all .mmdb files inside.
// Otherwise treats path as a single Country DB usable for both families.
// Supports both MaxMind (GeoLite2-Country, GeoLite2-ASN) and DBIP (dbip-country) formats.
// If downloadURLs is provided and downloadInterval > 0, will periodically download new MMDB files.
func NewFromPath(path string, reload time.Duration, downloadURLs []string, downloadInterval time.Duration) (Provider, func(), error) {
    m := &maxmind{path: path}
    load := func() error {
        m.loadMu.Lock()
        defer m.loadMu.Unlock()
        set, err := loadReaders(path)
        if err != nil {
            // keep serving from the previously loaded databases
            return err
        }
        m.mu.Lock()
        old := m.set
        m.set = set
        m.mu.Unlock()
        old.close()
        return nil
    }

    // Initial download if configured
    if downloadInterval > 0 && len(downloadURLs) > 0 {
        log.Printf("GeoIP: auto-download enabled (interval: %v)", downloadInterval)
        // Check if mmdb files exist, if not - download immediately
        if _, err := os.Stat(path); os.IsNotExist(err) {
            log.Printf("GeoIP: directory %s does not exist, performing initial download", path)
            if err := downloadMMDB(downloadURLs, path); err != nil {
                log.Printf("GeoIP: initial download error: %v", err)
            }
        } else {
            // Check if directory is empty
            entries, _ := os.ReadDir(path)
            mmdbCount := 0
            for _, e := range entries {
                if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mmdb") {
                    mmdbCount++
                }
            }
            if mmdbCount == 0 {
                log.Printf("GeoIP: no .mmdb files found in %s, performing initial download", path)
                if err := downloadMMDB(downloadURLs, path); err != nil {
                    log.Printf("GeoIP: initial download error: %v", err)
                }
            } else {
                log.Printf("GeoIP: found %d existing .mmdb file(s), skipping initial download", mmdbCount)
            }
        }
    }

    if err := load(); err != nil {
        // degrade to noop if cannot load but return error for logging upstream
        return NewNoop(), func() {}, err
    }
    stop := make(chan struct{})

    // Periodic reload goroutine
    go func() {
        if reload <= 0 { return }
        log.Printf("GeoIP: auto-reload enabled (interval: %v)", reload)
        ticker := time.NewTicker(reload)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                log.Printf("GeoIP: reloading databases...")
                _ = load()
            case <-stop:
                // best-effort close handled on next load call; nothing to do
                return
            }
        }
    }()

    // Periodic download goroutine
    go func() {
        if downloadInterval <= 0 || len(downloadURLs) == 0 { return }
        log.Printf("GeoIP: starting periodic download scheduler (interval: %v)", downloadInterval)
        ticker := time.NewTicker(downloadInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                log.Printf("GeoIP: periodic download triggered")
                if err := downloadMMDB(downloadURLs, path); err != nil {
                    log.Printf("GeoIP: download error: %v", err)
                }
                // Trigger reload after download
                log.Printf("GeoIP: reloading databases after download...")
                _ = load()
            case <-stop:
                log.Printf("GeoIP: stopping periodic download scheduler")
                return
            }
        }
    }()

    return m, func() { close(stop) }, nil
}

// loadReaders opens the databases at path. If path is a directory, all .mmdb files
// inside are loaded; otherwise path is a single Country DB used for both families.
func loadReaders(path string) (readerSet, error) {
    var s readerSet
    fi, err := os.Stat(path)
    if err != nil {
        return s, fmt.Errorf("stat %s: %w", path, err)
    }
    if fi.IsDir() {
        entries, err := os.ReadDir(path)
        if err != nil { return s, err }
        log.Printf("GeoIP: scanning directory %s for .mmdb files", path)
        for _, e := range entries {
            if e.IsDir() { continue }
            if !strings.HasSuffix(strings.ToLower(e.Name()), ".mmdb") { continue }
            full := filepath.Join(path, e.Name())
            log.Printf("GeoIP: attempting to load %s", full)

            var reader *dbReader
            var dbType string

            // Try geoip2 first (for MaxMind databases)
            if geoip2Reader, err := geoip2.Open(full); err == nil {
                dbType = strings.ToLower(geoip2Reader.Metadata().DatabaseType)
                reader = &dbReader{geoip2Reader: geoip2Reader, dbType: dbType}
                log.Printf("GeoIP: opened %s as geoip2 (type: %s)", e.Name(), dbType)
            } else {
                // Try maxminddb for other formats (like dbip)
                if rawReader, err := maxminddb.Open(full); err == nil {
                    dbType = strings.ToLower(rawReader.Metadata.DatabaseType)
                    reader = &dbReader{rawReader: rawReader, dbType: dbType}
                    log.Printf("GeoIP: opened %s as maxminddb (type: %s)", e.Name(), dbType)
                } else {
                    log.Printf("GeoIP: failed to open %s: %v", full, err)
                    continue
                }
            }

            name := strings.ToLower(e.Name())
            // Detect IPv6 hint in filename: ipv6, -6, _6, -v6, .6.mmdb
            is6Hint := strings.Contains(name, "ipv6") || strings.Contains(name, "-6") ||
                       strings.Contains(name, "_6") || strings.Contains(name, "-v6") ||
                       strings.HasSuffix(name, "6.mmdb")

            // Detect database type from metadata and filename
            isASN := strings.Contains(dbType, "asn") || strings.Contains(name, "asn")
            // Treat City DBs as valid sources for country/continent selection
            isCountry := strings.Contains(dbType, "country") || strings.Contains(name, "country") ||
                strings.Contains(dbType, "city") || strings.Contains(name, "city")

            log.Printf("GeoIP: file %s - is6Hint=%v, isASN=%v, isCountry=%v", e.Name(), is6Hint, isASN, isCountry)

            if isASN {
                if is6Hint {
                    replaceReader(&s.asn6, reader)
                    log.Printf("GeoIP: loaded ASN IPv6 DB %s", full)
                } else {
                    replaceReader(&s.asn4, reader)
                    log.Printf("GeoIP: loaded ASN IPv4 DB %s", full)
                }
            } else if isCountry {
                if is6Hint {
                    replaceReader(&s.country6, reader)
                    log.Printf("GeoIP: loaded Country IPv6 DB %s", full)
                } else {
                    replaceReader(&s.country4, reader)
                    log.Printf("GeoIP: loaded Country IPv4 DB %s", full)
                }
            } else {
                log.Printf("GeoIP: skipping unknown database type %s (file: %s)", dbType, e.Name())
                _ = reader.Close()
            }
        }
        // Universal MMDB (IPVersion=6) supports both IPv4+IPv6
        // Use loaded DBs as fallback for missing IP version
        if s.country4 == nil && s.country6 != nil {
            s.country4 = s.country6
            log.Printf("GeoIP: using IPv6 Country DB as fallback for IPv4")
        }
        if s.country6 == nil && s.country4 != nil {
            s.country6 = s.country4
            log.Printf("GeoIP: using IPv4 Country DB as fallback for IPv6")
        }
        if s.asn4 == nil && s.asn6 != nil {
            s.asn4 = s.asn6
            log.Printf("GeoIP: using IPv6 ASN DB as fallback for IPv4")
        }
        if s.asn6 == nil && s.asn4 != nil {
            s.asn6 = s.asn4
            log.Printf("GeoIP: using IPv4 ASN DB as fallback for IPv6")
        }
        // if none loaded, error
        if s.country4 == nil && s.country6 == nil && s.asn4 == nil && s.asn6 == nil {
            return s, errors.New("no geoip databases loaded")
        }
    } else {
        // Single file mode
        var reader *dbReader
        var dbType string
        if geoip2Reader, err := geoip2.Open(path); err == nil {
            dbType = strings.ToLower(geoip2Reader.Metadata().DatabaseType)
            reader = &dbReader{geoip2Reader: geoip2Reader, dbType: dbType}
            log.Printf("GeoIP: loaded geoip2 DB %s for IPv4/IPv6 (type: %s)", path, dbType)
        } else if rawReader, err := maxminddb.Open(path); err == nil {
            dbType = strings.ToLower(rawReader.Metadata.DatabaseType)
            reader = &dbReader{rawReader: rawReader, dbType: dbType}
            log.Printf("GeoIP: loaded maxminddb DB %s for IPv4/IPv6 (type: %s)", path, dbType)
        } else {
            return s, fmt.Errorf("open %s: %w", path, err)
        }
        // Use as both country4 and country6
        s.country4 = reader
        s.country6 = reader
    }
    return s, nil
}

// replaceReader stores r in slot, closing a reader a previous file put there.
func replaceReader(slot **dbReader, r *dbReader) {
    if *slot != nil {
        _ = (*slot).Close()
    }
    *slot = r
}

func (m *maxmind) readerFor(ip netip.Addr, which string) *dbReader {
    v6 := ip.Is6()
    switch which {
    case "country":
        if v6 {
            if r := m.set.country6; r != nil { return r }
            if r := m.set.country4; r != nil { return r }
        } else {
            if r := m.set.country4; r != nil { return r }
            if r := m.set.country6; r != nil { return r }
        }
    case "asn":
        if v6 {
            if r := m.set.asn6; r != nil { return r }
            if r := m.set.asn4; r != nil { return r }
        } else {
            if r := m.set.asn4; r != nil { return r }
            if r := m.set.asn6; r != nil { return r }
        }
    }
    return nil
}

func (m *maxmind) Lookup(ip netip.Addr) Info {
    m.mu.RLock()
    defer m.mu.RUnlock()
    var info Info
    nip := ip.AsSlice()

    if r := m.readerFor(ip, "country"); r != nil {
        if r.geoip2Reader != nil {
            // Use geoip2 API for MaxMind databases
            // If DB type is City, query City() to extract country/continent
            if strings.Contains(r.dbType, "city") {
                if rec, err := r.geoip2Reader.City(nip); err == nil && rec != nil {
                    info.Country = rec.Country.IsoCode
                    info.Continent = rec.Continent.Code
                }
            } else {
                if rec, err := r.geoip2Reader.Country(nip); err == nil && rec != nil {
                    info.Country = rec.Country.IsoCode
                    info.Continent = rec.Continent.Code
                }
            }
        } else if r.rawReader != nil {
            // Parse raw maxminddb data
            // Try DBIP format first (has country_code)
            var dbipRecord struct {
                CountryCode string `maxminddb:"country_code"`
            }
            if err := r.rawReader.Lookup(nip, &dbipRecord); err == nil && dbipRecord.CountryCode != "" {
                info.Country = dbipRecord.CountryCode
                // DBIP doesn't have continent, derive from country code
                info.Continent = continentFromCountry(dbipRecord.CountryCode)
            } else {
                // Try MaxMind format (has country.iso_code and continent.code)
                var mmRecord struct {
                    Country struct {
                        IsoCode string `maxminddb:"iso_code"`
                    } `maxminddb:"country"`
                    Continent struct {
                        Code string `maxminddb:"code"`
                    } `maxminddb:"continent"`
                }
                if err := r.rawReader.Lookup(nip, &mmRecord); err == nil {
                    info.Country = mmRecord.Country.IsoCode
                    info.Continent = mmRecord.Continent.Code
                }
            }
        }
    }

    if r := m.readerFor(ip, "asn"); r != nil {
        if r.geoip2Reader != nil {
            // Use geoip2 API
            if rec, err := r.geoip2Reader.ASN(nip); err == nil && rec != nil {
                info.ASN = int(rec.AutonomousSystemNumber)
            }
        } else if r.rawReader != nil {
            // Parse raw maxminddb data
            var record struct {
                ASN uint `maxminddb:"autonomous_system_number"`
            }
            if err := r.rawReader.Lookup(nip, &record); err == nil {
                info.ASN = int(record.ASN)
            }
        }
    }

    return info
}

// downloadMMDB downloads MMDB files from URLs to the target directory
func downloadMMDB(urls []string, targetDir string) error {
    if len(urls) == 0 {
        log.Printf("GeoIP: no download URLs configured, skipping download")
        return nil
    }

    log.Printf("GeoIP: starting download of %d file(s) to %s", len(urls), targetDir)

    // Ensure target directory exists
    if err := os.MkdirAll(targetDir, 0755); err != nil {
        return fmt.Errorf("create target dir: %w", err)
    }

    client := &http.Client{Timeout: 5 * time.Minute}
    downloaded := 0
    failed := 0

    for i, url := range urls {
        // Extract filename from URL
        parts := strings.Split(url, "/")
        filename := parts[len(parts)-1]
        if !strings.HasSuffix(strings.ToLower(filename), ".mmdb") {
            filename += ".mmdb"
        }

        targetPath := filepath.Join(targetDir, filename)
        tmpPath := targetPath + ".tmp"

        log.Printf("GeoIP: [%d/%d] downloading %s => %s", i+1, len(urls), url, filename)

        resp, err := client.Get(url)
        if err != nil {
            log.Printf("GeoIP: [%d/%d] failed to download %s: %v", i+1, len(urls), url, err)
            failed++
            continue
        }

        if resp.StatusCode != http.StatusOK {
            resp.Body.Close()
            log.Printf("GeoIP: [%d/%d] failed to download %s: HTTP %d", i+1, len(urls), url, resp.StatusCode)
            failed++
            continue
        }

        tmpFile, err := os.Create(tmpPath)
        if err != nil {
            resp.Body.Close()
            log.Printf("GeoIP: [%d/%d] failed to create temp file %s: %v", i+1, len(urls), tmpPath, err)
            failed++
            continue
        }

        size, err := io.Copy(tmpFile, resp.Body)
        resp.Body.Close()
        tmpFile.Close()

        if err != nil {
            os.Remove(tmpPath)
            log.Printf("GeoIP: [%d/%d] failed to save %s: %v", i+1, len(urls), filename, err)
            failed++
            continue
        }

        // Atomic rename
        if err := os.Rename(tmpPath, targetPath); err != nil {
            os.Remove(tmpPath)
            log.Printf("GeoIP: [%d/%d] failed to rename %s: %v", i+1, len(urls), filename, err)
            failed++
            continue
        }

        log.Printf("GeoIP: [%d/%d] downloaded %s successfully (%.2f MB)", i+1, len(urls), filename, float64(size)/(1024*1024))
        downloaded++
    }

    log.Printf("GeoIP: download completed: %d successful, %d failed", downloaded, failed)
    return nil
}
