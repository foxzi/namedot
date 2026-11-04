package db

import "testing"

func TestValidateRData(t *testing.T) {
	sp := func(s string) *string { return &s }
	cases := []struct {
		typ string
		r   RData
		ok  bool
	}{
		{"A", RData{Data: "192.0.2.1"}, true},
		{"A", RData{Data: "not-an-ip"}, false},
		{"A", RData{Data: "2001:db8::1"}, false},
		{"AAAA", RData{Data: "2001:db8::1"}, true},
		{"MX", RData{Data: "10 mail.example.com."}, true},
		{"MX", RData{Data: "mail.example.com."}, false},
		{"TXT", RData{Data: `"hello world"`}, true},
		{"CNAME", RData{Data: "target.example.com."}, true},
		{"A", RData{Data: ""}, false},
		{"A", RData{Data: "192.0.2.1\nexample. 300 IN A 192.0.2.2"}, false},
		{"A", RData{Data: "192.0.2.1", Subnet: sp("10.0.0.0/8")}, true},
		{"A", RData{Data: "192.0.2.1", Subnet: sp("10.0.0.0/99")}, false},
		{"A", RData{Data: "192.0.2.1", Country: sp("RUS")}, false},
		{"A", RData{Data: "192.0.2.1", Continent: sp("EU")}, true},
	}
	for _, tc := range cases {
		err := ValidateRData(tc.typ, tc.r)
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: ok=%v, err=%v", tc.typ, tc.r.Data, tc.ok, err)
		}
	}
}
