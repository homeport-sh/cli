package main

import (
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
		{Domain: "*.saas.example", Status: "live", Certificate: "per-name", CertificateWhy: "no CNAME at _acme-challenge.saas.example",
			Records:   []cloud.DNSRecord{{Type: "CNAME", Name: "_acme-challenge.saas", FullName: "_acme-challenge.saas.example", Value: "abc.acme.homeport.test"}},
			CreatedAt: "2026-10-01T12:00:00Z"},
		{Domain: "*.one.example", Status: "live", Certificate: "wildcard", Records: []cloud.DNSRecord{}, CreatedAt: "2026-10-01T12:00:00Z"},
	}
	if code := h.run("domains", "get", "*.saas.example"); code != 0 {
		t.Fatalf("get: %d %s", code, h.said())
	}
	said := h.said()
	for _, want := range []string{"Each name under it gets its own certificate", "no CNAME at _acme-challenge.saas.example", "_acme-challenge.saas", "abc.acme.homeport.test"} {
		if !strings.Contains(said, want) {
			t.Errorf("get didn't say %q: %s", want, said)
		}
	}
	h.out.Reset()
	if code := h.run("domains", "get", "*.one.example"); code != 0 || !strings.Contains(h.said(), "Every name under it is served one wildcard certificate") {
		t.Fatalf("get: %d %s", code, h.said())
	}
}
