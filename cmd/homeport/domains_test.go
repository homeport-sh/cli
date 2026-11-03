package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/internal/cloud"
)

// A platform's tenants' domains: added, shown with the DNS records to set,
// listed across pages, and removed - asked first.
func TestDomainsAreAddedListedAndRemoved(t *testing.T) {
	f, h := linkedHarness(t)
	f.domains = []cloud.Domain{
		{Domain: "a.example.com", Status: "live", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z"},
		{Domain: "b.example.com", Status: "failed", Why: "its CNAME points elsewhere", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-02T12:00:00Z"},
	}
	if code := h.run("domains", "add", "shop.acme.com"); code != 0 {
		t.Fatalf("add: %d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"Added shop.acme.com", "alice/blog (production)", "pending", "CNAME", "shop", "blog.homeport.app", "TXT", "_homeport.shop", "homeport-verify=abc123", "2026-10-29"} {
		if !strings.Contains(said, want) {
			t.Errorf("add didn't say %q: %s", want, said)
		}
	}

	h.out.Reset()
	if code := h.run("domains", "ls"); code != 0 {
		t.Fatalf("ls: %d %s", code, h.said())
	}
	said = h.said()
	for _, want := range []string{"DOMAIN", "STATUS", "a.example.com", "live", "b.example.com", "its CNAME points elsewhere", "shop.acme.com", "pending", "1 live of 3", "100"} {
		if !strings.Contains(said, want) {
			t.Errorf("ls didn't say %q: %s", want, said)
		}
	}
	// every page, the second after the first's last
	if got := strings.Join(f.domainCalls, "|"); !strings.Contains(got, "GET "+blogID+" |GET "+blogID+" b.example.com") {
		t.Errorf("calls %s", got)
	}

	h.out.Reset()
	if code := h.run("domains", "get", "b.example.com"); code != 0 || !strings.Contains(h.said(), "failed") || !strings.Contains(h.said(), "its CNAME points elsewhere") {
		t.Fatalf("get: %d %s", code, h.said())
	}
	if code := h.run("domains", "get", "nope.example.com"); code == 0 || !strings.Contains(h.said(), "no such domain") {
		t.Fatalf("get of none: %d %s", code, h.said())
	}

	// without a terminal, a removal needs --yes
	h.out.Reset()
	if code := h.run("domains", "remove", "a.example.com"); code != exitUsage || !strings.Contains(h.said(), "--yes") || len(f.domains) != 3 {
		t.Fatalf("remove without --yes: %d %s %v", code, h.said(), f.domains)
	}
	if code := h.run("domains", "remove", "a.example.com", "--yes"); code != 0 || !strings.Contains(h.said(), "Removed a.example.com") || len(f.domains) != 2 {
		t.Fatalf("remove: %d %s %v", code, h.said(), f.domains)
	}
}

// A domain is checked here before anything is sent: one name, no scheme,
// no path; and it reaches the API as a path segment.
func TestDomainsRefusesWhatIsntOne(t *testing.T) {
	f, h := linkedHarness(t)
	for _, args := range [][]string{
		{"domains"},
		{"domains", "add"},
		{"domains", "add", "a.example.com", "b.example.com"},
		{"domains", "add", "https://a.example.com"},
		{"domains", "get", "a.example.com/x"},
		{"domains", "remove", "../env", "--yes"},
		{"domains", "frob"},
	} {
		if code := h.run(args...); code != exitUsage {
			t.Errorf("%v: %d %s", args, code, h.said())
		}
	}
	if len(f.domainCalls) != 0 {
		t.Fatalf("sent %v", f.domainCalls)
	}
	if code := h.run("domains", "add", "Shop.Acme.com."); code != 0 || len(f.domains) != 1 || f.domains[0].Domain != "shop.acme.com" {
		t.Fatalf("a name as typed: %d %s %v", code, h.said(), f.domains)
	}
}

// The API's refusal is said as it is.
func TestDomainsSaysWhyItWasRefused(t *testing.T) {
	_, h := linkedHarness(t)
	if code := h.run("domains", "add", "localhost"); code == 0 || !strings.Contains(h.said(), "that isn't a domain") {
		t.Fatalf("%d %s", code, h.said())
	}
}

// A live wildcard says whether its tenants share its one certificate, and
// when they don't, the record that would and why it isn't there yet.
func TestADomainSaysItsCertificate(t *testing.T) {
	f, h := linkedHarness(t)
	f.domains = []cloud.Domain{
		{Domain: "*.saas.example", Status: "live", Certificate: "lapsing", CertificateWhy: "no CNAME at _acme-challenge.saas.example",
			Records:   []cloud.DNSRecord{{Type: "CNAME", Name: "_acme-challenge.saas", FullName: "_acme-challenge.saas.example", Value: "abc.acme.homeport.test"}},
			CreatedAt: "2026-10-01T12:00:00Z"},
		{Domain: "*.one.example", Status: "live", Certificate: "wildcard", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z"},
	}
	if code := h.run("domains", "get", "*.saas.example"); code != 0 {
		t.Fatalf("get: %d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"lapsing: the certificate it has serves on until it expires, and isn't renewed", "no CNAME at _acme-challenge.saas.example", "_acme-challenge.saas", "abc.acme.homeport.test"} {
		if !strings.Contains(said, want) {
			t.Errorf("get didn't say %q: %s", want, said)
		}
	}
	h.out.Reset()
	if code := h.run("domains", "get", "*.one.example"); code != 0 || !strings.Contains(h.said(), "Every name under it is served one wildcard certificate") {
		t.Fatalf("get: %d %s", code, h.said())
	}
}

// A pending domain whose DNS provider can set its records itself says so,
// and `domains connect` opens the provider's page to sign in and confirm.
// Without a terminal the link is said, not opened; a provider that can't
// is the API's refusal.
func TestADomainConnectsAtItsDNSProvider(t *testing.T) {
	f, h := linkedHarness(t)
	f.domains = []cloud.Domain{
		{Domain: "shop.acme.com", Status: "pending", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z",
			Connect: &cloud.DomainConnect{Provider: "Cloudflare", Available: true}},
		{Domain: "b.example.com", Status: "pending", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z",
			Connect: &cloud.DomainConnect{Provider: "Namecheap"}},
		{Domain: "live.example.com", Status: "live", Unrouted: "its A record points at 192.0.2.1, not homeport",
			Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z", Connect: &cloud.DomainConnect{Provider: "Cloudflare", Available: true}},
	}
	if code := h.run("domains", "get", "shop.acme.com"); code != 0 {
		t.Fatalf("get: %d %s", code, h.said())
	}
	if said := h.said(); !strings.Contains(said, "Its DNS is at Cloudflare") || !strings.Contains(said, "homeport domains connect shop.acme.com") {
		t.Fatalf("get didn't offer connect: %s", said)
	}
	if code := h.run("domains", "get", "b.example.com"); code != 0 || strings.Contains(h.said(), "domains connect") {
		t.Fatalf("offered a provider that can't: %d %s", code, h.said())
	}
	if code := h.run("domains", "get", "live.example.com"); code != 0 || !strings.Contains(h.said(), "Not reachable yet: its A record points at 192.0.2.1") ||
		!strings.Contains(h.said(), "homeport domains connect live.example.com") {
		t.Fatalf("unrouted: %d %s", code, h.said())
	}

	link := "https://dc.example/v2/domainTemplates/providers/homeport.sh?domain=shop.acme.com"
	if code := h.run("domains", "connect", "shop.acme.com"); code != 0 || len(h.opened) != 0 || !strings.Contains(h.said(), link) {
		t.Fatalf("without a terminal: %d %v %s", code, h.opened, h.said())
	}
	h.a.tty = true
	if code := h.run("domains", "connect", "Shop.Acme.com"); code != 0 || len(h.opened) != 1 || h.opened[0] != link || !strings.Contains(h.said(), "Opened") {
		t.Fatalf("with one: %d %v %s", code, h.opened, h.said())
	}
	if code := h.run("domains", "connect", "shop.acme.com", "--no-browser"); code != 0 || len(h.opened) != 1 {
		t.Fatalf("--no-browser: %d %v %s", code, h.opened, h.said())
	}
	if code := h.run("domains", "connect", "b.example.com"); code == 0 || !strings.Contains(h.said(), "can't set its records itself") {
		t.Fatalf("refused: %d %s", code, h.said())
	}
	if got := f.domainCalls[len(f.domainCalls)-1]; got != "POST "+blogID+" /b.example.com/connect" {
		t.Errorf("call %s", got)
	}
}

// ls lists the app's own domains (its Domains tab's) too, before its
// tenants': how each is reached and where it redirects.
func TestDomainsLsListsTheAppsOwnDomains(t *testing.T) {
	f, h := linkedHarness(t)
	f.customDomains = []cloud.CustomDomain{
		{Domain: "blog.example.com", Status: "live", Proxied: true},
		{Domain: "www.blog.example.com", Status: "live", RedirectTo: "blog.example.com"},
		{Domain: "new.example.com", Status: "pending", Why: "it points at 192.0.2.1, not 203.0.113.50"},
	}
	if code := h.run("domains", "ls"); code != 0 {
		t.Fatalf("ls: %d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{
		"blog.example.com      live     proxied",
		"www.blog.example.com  live     direct   → blog.example.com",
		"new.example.com       pending           it points at 192.0.2.1",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("ls didn't say %q: %s", want, said)
		}
	}
	if strings.Contains(said, "no platform domains") {
		t.Errorf("ls said it has no domains: %s", said)
	}

	// neither kind: what adds each
	f.customDomains = nil
	h.out.Reset()
	if code := h.run("domains", "ls"); code != 0 || !strings.Contains(h.said(), "has no domains") {
		t.Fatalf("ls of none: %d %s", code, h.said())
	}
}

// `domains proxy <domain> on|off` switches one of the app's own live
// domains into its DNS provider's proxy or out of it: the provider's page
// opened (or said), and on says first what the proxy needs. One that can't
// be switched is the API's refusal.
func TestADomainsProxyIsSwitched(t *testing.T) {
	f, h := linkedHarness(t)
	f.customDomains = []cloud.CustomDomain{
		{Domain: "blog.example.com", Status: "live"},
		{Domain: "new.example.com", Status: "pending"},
	}
	link := "https://dc.example/v2/domainTemplates/providers/homeport.sh/services/subdomain-proxied/apply?domain=example.com&host=blog"
	if code := h.run("domains", "proxy", "blog.example.com", "on"); code != 0 || len(h.opened) != 0 ||
		!strings.Contains(h.said(), link) || !strings.Contains(h.said(), "Full (strict)") {
		t.Fatalf("on, without a terminal: %d %v %s", code, h.opened, h.said())
	}
	h.a.tty = true
	if code := h.run("domains", "proxy", "Blog.Example.com", "on"); code != 0 || len(h.opened) != 1 || h.opened[0] != link || !strings.Contains(h.said(), "Opened") {
		t.Fatalf("on, with one: %d %v %s", code, h.opened, h.said())
	}
	if code := h.run("domains", "proxy", "blog.example.com", "off", "--no-browser"); code == 0 || len(h.opened) != 1 ||
		!strings.Contains(h.said(), "it isn't behind its DNS provider's proxy") {
		t.Fatalf("off, direct already: %d %v %s", code, h.opened, h.said())
	}
	if code := h.run("domains", "proxy", "new.example.com", "on"); code == 0 || !strings.Contains(h.said(), "only a live domain's proxy can be switched") {
		t.Fatalf("pending: %d %s", code, h.said())
	}
	want := []string{"POST blog.example.com on=true", "POST blog.example.com on=true", "POST blog.example.com on=false", "POST new.example.com on=true"}
	if !slices.Equal(f.proxyCalls, want) {
		t.Errorf("calls %v", f.proxyCalls)
	}
	for _, args := range [][]string{{"blog.example.com"}, {"blog.example.com", "maybe"}, {"on"}} {
		if code := h.run(append([]string{"domains", "proxy"}, args...)...); code == 0 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if len(f.proxyCalls) != len(want) {
		t.Errorf("a bad call reached the API: %v", f.proxyCalls)
	}
}
