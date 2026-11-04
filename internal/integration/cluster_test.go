package integration

// Local cluster test: real namedot processes, two independent masters with
// their own slaves and different zones. Each node runs as a separate process
// because the DNS server registers its handler on the global dns mux.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const clusterToken = "cluster-token"

type node struct {
	name    string
	dnsAddr string
	restURL string
	cmd     *exec.Cmd
	log     string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func buildNamedot(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "namedot")
	out, err := exec.Command("go", "build", "-o", bin, "namedot/cmd/namedot").CombinedOutput()
	if err != nil {
		t.Fatalf("build namedot: %v\n%s", err, out)
	}
	return bin
}

// startNode writes a config for a master (masterURL == "") or a slave and runs the binary.
func startNode(t *testing.T, bin, name, masterURL string) *node {
	t.Helper()
	dir := t.TempDir()
	dnsPort, restPort := freePort(t), freePort(t)
	repl := `  mode: "master"`
	if masterURL != "" {
		repl = fmt.Sprintf("  mode: \"slave\"\n  master_url: %q\n  sync_interval_sec: 1\n  api_token: %q", masterURL, clusterToken)
	}
	cfg := fmt.Sprintf(`listen: "127.0.0.1:%d"
forwarder: ""
api_token: %q
rest_listen: "127.0.0.1:%d"
default_ttl: 300
soa:
  auto_on_missing: true
  primary: "ns1.{zone}"
  hostmaster: "hostmaster.{zone}"
db:
  driver: "sqlite"
  dsn: "file:%s?_foreign_keys=on"
geoip:
  enabled: false
  use_ecs: true
log:
  dns_verbose: false
admin:
  enabled: false
replication:
%s
`, dnsPort, clusterToken, restPort, filepath.Join(dir, "node.db"), repl)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	n := &node{
		name:    name,
		dnsAddr: fmt.Sprintf("127.0.0.1:%d", dnsPort),
		restURL: fmt.Sprintf("http://127.0.0.1:%d", restPort),
		log:     filepath.Join(dir, "node.log"),
	}
	logf, err := os.Create(n.log)
	if err != nil {
		t.Fatal(err)
	}
	n.cmd = exec.Command(bin, "-c", cfgPath)
	n.cmd.Dir = dir
	n.cmd.Stdout, n.cmd.Stderr = logf, logf
	if err := n.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		n.stop()
		logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(n.log)
			t.Logf("--- %s log ---\n%s", name, b)
		}
	})
	if err := waitHTTPReady(n.restURL+"/health", 10*time.Second); err != nil {
		t.Fatalf("%s not ready: %v", name, err)
	}
	return n
}

func (n *node) stop() {
	if n.cmd.Process != nil && n.cmd.ProcessState == nil {
		_ = n.cmd.Process.Signal(os.Interrupt)
		_ = n.cmd.Wait()
	}
}

func (n *node) api(t *testing.T, method, path, body string, want int) []byte {
	t.Helper()
	req, _ := http.NewRequest(method, n.restURL+path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+clusterToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v", n.name, method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s %s: status %d, want %d: %s", n.name, method, path, resp.StatusCode, want, buf.String())
	}
	return buf.Bytes()
}

func (n *node) createZone(t *testing.T, name string) uint {
	var z struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(n.api(t, "POST", "/zones", fmt.Sprintf(`{"name":%q}`, name), http.StatusCreated), &z); err != nil {
		t.Fatal(err)
	}
	return z.ID
}

func (n *node) addRRSet(t *testing.T, zoneID uint, rrset string) uint {
	var rr struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(n.api(t, "POST", fmt.Sprintf("/zones/%d/rrsets", zoneID), rrset, http.StatusCreated), &rr); err != nil {
		t.Fatal(err)
	}
	return rr.ID
}

// answer is a comparable summary of a DNS response.
type answer struct {
	rcode  int
	answer string
	soa    string // owner and serial of the SOA in the authority section
}

func (a answer) String() string {
	return fmt.Sprintf("rcode=%s answer=[%s] soa=%q", dns.RcodeToString[a.rcode], a.answer, a.soa)
}

func query(addr, name string, qtype uint16, ecs string) (answer, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	if ecs != "" {
		o := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
		o.SetUDPSize(4096)
		o.Option = append(o.Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 32, Address: net.ParseIP(ecs).To4()})
		m.Extra = append(m.Extra, o)
	}
	in, _, err := (&dns.Client{Timeout: 2 * time.Second}).Exchange(m, addr)
	if err != nil {
		return answer{}, err
	}
	var rrs []string
	for _, rr := range in.Answer {
		h := *rr.Header()
		rrs = append(rrs, strings.TrimPrefix(rr.String(), h.String()))
	}
	sort.Strings(rrs)
	a := answer{rcode: in.Rcode, answer: strings.Join(rrs, "; ")}
	for _, rr := range in.Ns {
		if soa, ok := rr.(*dns.SOA); ok {
			a.soa = fmt.Sprintf("%s %d", soa.Hdr.Name, soa.Serial)
		}
	}
	return a, nil
}

type check struct {
	name  string
	qtype uint16
	ecs   string
}

// expect polls addr until every check returns the wanted answer.
func expect(t *testing.T, n *node, want map[check]string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var bad []string
		for c, w := range want {
			got, err := query(n.dnsAddr, c.name, c.qtype, c.ecs)
			if err != nil || got.String() != w {
				bad = append(bad, fmt.Sprintf("%s %s ecs=%q: got %v (err %v), want %s", c.name, dns.TypeToString[c.qtype], c.ecs, got, err, w))
			}
		}
		if len(bad) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not converge:\n%s", n.name, strings.Join(bad, "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// snapshot returns the current answers of n for the given checks.
func snapshot(t *testing.T, n *node, checks []check) map[check]string {
	t.Helper()
	out := make(map[check]string, len(checks))
	for _, c := range checks {
		a, err := query(n.dnsAddr, c.name, c.qtype, c.ecs)
		if err != nil {
			t.Fatalf("%s query %s: %v", n.name, c.name, err)
		}
		out[c] = a.String()
	}
	return out
}

func TestCluster_MastersAndSlaves(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster test builds and runs namedot processes")
	}
	bin := buildNamedot(t)

	// Cluster A: example.com and its child zone sub.example.com.
	masterA := startNode(t, bin, "master-a", "")
	com := masterA.createZone(t, "example.com")
	wwwID := masterA.addRRSet(t, com, `{"name":"www","type":"A","ttl":60,"records":[{"data":"192.0.2.1"}]}`)
	masterA.addRRSet(t, com, `{"name":"www","type":"AAAA","ttl":60,"records":[{"data":"2001:db8::1"}]}`)
	masterA.addRRSet(t, com, `{"name":"@","type":"MX","ttl":60,"records":[{"data":"10 mail.example.com."},{"data":"20 mx2.example.com."}]}`)
	masterA.addRRSet(t, com, `{"name":"@","type":"TXT","ttl":60,"records":[{"data":"\"v=spf1 -all\""}]}`)
	masterA.addRRSet(t, com, `{"name":"alias","type":"CNAME","ttl":60,"records":[{"data":"www.example.com."}]}`)
	masterA.addRRSet(t, com, `{"name":"*.wild","type":"A","ttl":60,"records":[{"data":"192.0.2.99"}]}`)
	sub := masterA.createZone(t, "sub.example.com")
	masterA.addRRSet(t, sub, `{"name":"host","type":"A","ttl":60,"records":[{"data":"192.0.2.50"}]}`)

	// Cluster B: example.org with geo records and example.net.
	masterB := startNode(t, bin, "master-b", "")
	org := masterB.createZone(t, "example.org")
	masterB.addRRSet(t, org, `{"name":"geo","type":"A","ttl":60,"records":[{"data":"198.51.100.1","subnet":"10.0.0.0/8"},{"data":"198.51.100.2"}]}`)
	net_ := masterB.createZone(t, "example.net")
	masterB.addRRSet(t, net_, `{"name":"www","type":"A","ttl":60,"records":[{"data":"203.0.113.7"}]}`)

	slaveA1 := startNode(t, bin, "slave-a1", masterA.restURL)
	slaveA2 := startNode(t, bin, "slave-a2", masterA.restURL)
	slaveB1 := startNode(t, bin, "slave-b1", masterB.restURL)

	checksA := []check{
		{"www.example.com.", dns.TypeA, ""},
		{"www.example.com.", dns.TypeAAAA, ""},
		{"example.com.", dns.TypeMX, ""},
		{"example.com.", dns.TypeTXT, ""},
		{"alias.example.com.", dns.TypeCNAME, ""},
		{"x.wild.example.com.", dns.TypeA, ""},
		{"host.sub.example.com.", dns.TypeA, ""},
		{"nope.example.com.", dns.TypeA, ""},     // NXDOMAIN with SOA
		{"www.example.com.", dns.TypeTXT, ""},    // NODATA with SOA
		{"nope.sub.example.com.", dns.TypeA, ""}, // SOA of the child zone
	}
	checksB := []check{
		{"geo.example.org.", dns.TypeA, ""},
		{"geo.example.org.", dns.TypeA, "10.1.2.3"},
		{"www.example.net.", dns.TypeA, ""},
		{"nope.example.org.", dns.TypeA, ""},
	}

	t.Run("masters serve their own zones", func(t *testing.T) {
		wantA := snapshot(t, masterA, checksA)
		for c, want := range map[check]string{
			checksA[0]: "rcode=NOERROR answer=[192.0.2.1] soa=\"\"",
			checksA[2]: "rcode=NOERROR answer=[10 mail.example.com.; 20 mx2.example.com.] soa=\"\"",
			checksA[5]: "rcode=NOERROR answer=[192.0.2.99] soa=\"\"",
			checksA[6]: "rcode=NOERROR answer=[192.0.2.50] soa=\"\"",
		} {
			if wantA[c] != want {
				t.Errorf("master-a %s %s: got %s, want %s", c.name, dns.TypeToString[c.qtype], wantA[c], want)
			}
		}
		for _, c := range checksA[7:] {
			if !strings.HasPrefix(wantA[c], "rcode=") || strings.HasSuffix(wantA[c], `soa=""`) {
				t.Errorf("master-a %s: negative answer without SOA: %s", c.name, wantA[c])
			}
		}
		if !strings.Contains(wantA[checksA[9]], `soa="sub.example.com.`) {
			t.Errorf("child zone miss must carry child SOA: %s", wantA[checksA[9]])
		}
		wantB := snapshot(t, masterB, checksB)
		if !strings.Contains(wantB[checksB[0]], "198.51.100.2") || !strings.Contains(wantB[checksB[1]], "198.51.100.1") {
			t.Errorf("geo selection by ECS: plain %s, ecs %s", wantB[checksB[0]], wantB[checksB[1]])
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("slaves replicate only their master", func(t *testing.T) {
		wantA := snapshot(t, masterA, checksA)
		expect(t, slaveA1, wantA)
		expect(t, slaveA2, wantA)
		expect(t, slaveB1, snapshot(t, masterB, checksB))

		// Zones of the other cluster are unknown: plain NXDOMAIN, no SOA.
		foreign := answer{rcode: dns.RcodeNameError}.String()
		expect(t, slaveB1, map[check]string{{"www.example.com.", dns.TypeA, ""}: foreign})
		expect(t, slaveA1, map[check]string{{"www.example.net.", dns.TypeA, ""}: foreign})
	})

	t.Run("changes on master reach slaves", func(t *testing.T) {
		masterA.api(t, "PUT", fmt.Sprintf("/zones/%d/rrsets/%d", com, wwwID),
			`{"name":"www","type":"A","ttl":60,"records":[{"data":"192.0.2.2"},{"data":"192.0.2.3"}]}`, http.StatusOK)
		shop := masterA.createZone(t, "shop.example.com")
		masterA.addRRSet(t, shop, `{"name":"@","type":"A","ttl":60,"records":[{"data":"192.0.2.77"}]}`)
		masterA.api(t, "DELETE", fmt.Sprintf("/zones/%d", sub), "", http.StatusNoContent)

		checks := append(checksA, check{"shop.example.com.", dns.TypeA, ""})
		want := snapshot(t, masterA, checks)
		if !strings.Contains(want[checksA[0]], "192.0.2.2; 192.0.2.3") {
			t.Fatalf("master-a did not apply update: %s", want[checksA[0]])
		}
		// After deleting the child zone its names belong to example.com.
		if !strings.Contains(want[checksA[6]], `NXDOMAIN`) || !strings.Contains(want[checksA[6]], `soa="example.com.`) {
			t.Fatalf("deleted child zone must fall back to parent: %s", want[checksA[6]])
		}
		expect(t, slaveA1, want)
		expect(t, slaveA2, want)
	})

	t.Run("slave keeps serving when master is down", func(t *testing.T) {
		want := snapshot(t, slaveA1, checksA)
		masterA.stop()
		time.Sleep(1500 * time.Millisecond) // at least one failed sync
		expect(t, slaveA1, want)
	})
}
