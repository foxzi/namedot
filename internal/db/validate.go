package db

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/miekg/dns"
)

// ValidateRData checks that data is valid RDATA for rrType, the same way the DNS
// server parses it when answering, and that GeoIP selectors are well-formed.
func ValidateRData(rrType string, r RData) error {
	data := strings.TrimSpace(r.Data)
	if data == "" {
		return errors.New("record data is empty")
	}
	if strings.ContainsAny(data, "\r\n") {
		return errors.New("record data must be a single line")
	}
	rr, err := dns.NewRR(fmt.Sprintf(". 300 IN %s %s", strings.ToUpper(rrType), data))
	if err != nil || rr == nil {
		return fmt.Errorf("invalid %s data %q: %v", rrType, data, err)
	}
	if r.Country != nil && *r.Country != "" && len(*r.Country) != 2 {
		return fmt.Errorf("country must be a 2-letter code, got %q", *r.Country)
	}
	if r.Continent != nil && *r.Continent != "" && len(*r.Continent) != 2 {
		return fmt.Errorf("continent must be a 2-letter code, got %q", *r.Continent)
	}
	if r.Subnet != nil && *r.Subnet != "" {
		if _, err := netip.ParsePrefix(*r.Subnet); err != nil {
			return fmt.Errorf("invalid subnet %q: %v", *r.Subnet, err)
		}
	}
	return nil
}

// ValidateRRSet validates every record of set against its type.
func ValidateRRSet(set RRSet) error {
	for _, r := range set.Records {
		if err := ValidateRData(set.Type, r); err != nil {
			return err
		}
	}
	return nil
}
