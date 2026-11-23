package deps

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// batch is how many packages one querybatch asks about: OSV's limit.
const batch = 1000

// maxVulns is past the advisories any one release has; past it, the rest
// are left unread.
const maxVulns = 500

// OSV asks osv.dev - the open vulnerability database, with npm's and
// Packagist's advisories (GitHub's and FriendsOfPHP's among them) - with
// no account.
type OSV struct {
	Client *http.Client // nil: a 30-second client
	Base   string       // "" is https://api.osv.dev
}

// Finding is a vulnerable package and its advisories.
type Finding struct {
	Dep   Dep    `json:"dep"`
	Vulns []Vuln `json:"vulns"`
}

// Vuln is one advisory: its ID (GHSA-…, a CVE among its aliases), what it
// says, its severity where the database says one (LOW, MODERATE, HIGH,
// CRITICAL), and the versions that fix it for the package - none yet,
// when it's unfixed.
type Vuln struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Fixed    []string `json:"fixed,omitempty"`
}

// Check is each of ds with a known vulnerability, in ds's order, with its
// advisories by ID. OSV not answering is an error, never a clean bill.
func (o *OSV) Check(ctx context.Context, ds []Dep) ([]Finding, error) {
	ids := make([][]string, len(ds))
	for lo := 0; lo < len(ds); lo += batch {
		part := ds[lo:min(lo+batch, len(ds))]
		got, err := o.queryBatch(ctx, part)
		if err != nil {
			return nil, err
		}
		copy(ids[lo:], got)
	}
	var want []string
	for _, x := range ids {
		for _, id := range x {
			if !slices.Contains(want, id) && len(want) < maxVulns {
				want = append(want, id)
			}
		}
	}
	details, err := o.vulns(ctx, want)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for i, d := range ds {
		var vs []Vuln
		for _, id := range ids[i] {
			if r, ok := details[id]; ok {
				vs = append(vs, r.vuln(d))
			}
		}
		if len(vs) > 0 {
			slices.SortFunc(vs, func(a, b Vuln) int { return cmp.Compare(a.ID, b.ID) })
			out = append(out, Finding{Dep: d, Vulns: vs})
		}
	}
	return out, nil
}

func (o *OSV) base() string { return cmp.Or(strings.TrimSuffix(o.Base, "/"), "https://api.osv.dev") }

func (o *OSV) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// queryBatch is the IDs of the advisories that cover each of ds.
func (o *OSV) queryBatch(ctx context.Context, ds []Dep) ([][]string, error) {
	type pkg struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	}
	type query struct {
		Package pkg    `json:"package"`
		Version string `json:"version"`
	}
	var q struct {
		Queries []query `json:"queries"`
	}
	for _, d := range ds {
		q.Queries = append(q.Queries, query{pkg{d.Name, d.Ecosystem}, d.Version})
	}
	body, _ := json.Marshal(q)
	var res struct {
		Results []struct {
			Vulns []struct {
				ID string `json:"id"`
			} `json:"vulns"`
		} `json:"results"`
	}
	if err := o.do(ctx, http.MethodPost, "/v1/querybatch", body, &res); err != nil {
		return nil, err
	}
	if len(res.Results) != len(ds) {
		return nil, fmt.Errorf("osv.dev answered %d of %d packages", len(res.Results), len(ds))
	}
	out := make([][]string, len(ds))
	for i, r := range res.Results {
		for _, v := range r.Vulns {
			out[i] = append(out[i], v.ID)
		}
	}
	return out, nil
}

// osvRecord is an advisory as OSV gives it: what Vuln says, read.
type osvRecord struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases"`
	Summary  string   `json:"summary"`
	Details  string   `json:"details"`
	Database struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Affected []struct {
		Package struct{ Ecosystem, Name string } `json:"package"`
		Ranges  []struct {
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

// vuln is r as it's d's: its fixed versions d's package's alone.
func (r osvRecord) vuln(d Dep) Vuln {
	v := Vuln{ID: r.ID, Aliases: r.Aliases, Summary: oneLine(cmp.Or(r.Summary, r.Details)), Severity: strings.ToUpper(r.Database.Severity)}
	for _, a := range r.Affected {
		if a.Package.Ecosystem != d.Ecosystem || a.Package.Name != d.Name {
			continue
		}
		for _, rg := range a.Ranges {
			for _, e := range rg.Events {
				if e.Fixed != "" && !slices.Contains(v.Fixed, e.Fixed) {
					v.Fixed = append(v.Fixed, e.Fixed)
				}
			}
		}
	}
	slices.Sort(v.Fixed)
	return v
}

// oneLine is s's first line, at most 200 bytes, for a log line.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// vulns reads each advisory, a few at a time.
func (o *OSV) vulns(ctx context.Context, ids []string) (map[string]osvRecord, error) {
	out := map[string]osvRecord{}
	var mu sync.Mutex
	var first error
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			var r osvRecord
			err := o.do(ctx, http.MethodGet, "/v1/vulns/"+url.PathEscape(id), nil, &r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				first = cmp.Or(first, err)
				return
			}
			out[id] = r
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}

// do sends body (nil none) and reads the JSON answer into v.
func (o *OSV) do(ctx context.Context, method, path string, body []byte, v any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base()+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := o.client().Do(req)
	if err != nil {
		return fmt.Errorf("osv.dev: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("osv.dev: %s %s: %s", method, path, res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(v); err != nil {
		return fmt.Errorf("osv.dev: %s %s: %w", method, path, err)
	}
	return nil
}
