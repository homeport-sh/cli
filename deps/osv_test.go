package deps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOSV answers querybatch with known vulnerabilities by
// ecosystem/name@version, and each vulnerability's details.
func fakeOSV(t *testing.T, known map[string][]string, details map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
			var q struct {
				Queries []struct {
					Package struct{ Name, Ecosystem string }
					Version string
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Errorf("querybatch: %v", err)
			}
			if len(q.Queries) > batch {
				t.Errorf("querybatch of %d, past %d", len(q.Queries), batch)
			}
			var out struct {
				Results []map[string]any `json:"results"`
			}
			for _, x := range q.Queries {
				var vs []map[string]string
				for _, id := range known[x.Package.Ecosystem+"/"+x.Package.Name+"@"+x.Version] {
					vs = append(vs, map[string]string{"id": id})
				}
				res := map[string]any{}
				if vs != nil {
					res["vulns"] = vs
				}
				out.Results = append(out.Results, res)
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			d, ok := details[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(d))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// Check asks OSV about every package, in batches, and says each
// vulnerable one's advisories: their summary, severity and the versions
// that fix them - for its own package.
func TestCheckSaysWhatsVulnerableAndWhatFixesIt(t *testing.T) {
	ds := []Dep{{"Packagist", "laravel/framework", "11.9.0"}, {"npm", "lodash", "4.17.20"}, {"npm", "react", "18.2.0"}}
	for i := range batch + 5 { // past one batch
		ds = append(ds, Dep{"npm", "pad", "1.0." + strings.Repeat("0", i%3) + string(rune('0'+i%10))})
	}
	srv := fakeOSV(t, map[string][]string{
		"Packagist/laravel/framework@11.9.0": {"GHSA-aaaa"},
		"npm/lodash@4.17.20":                 {"GHSA-bbbb", "GHSA-cccc"},
	}, map[string]string{
		"GHSA-aaaa": `{"id":"GHSA-aaaa","summary":"Laravel lets a signed URL be forged","aliases":["CVE-2026-1"],
			"database_specific":{"severity":"HIGH"},
			"affected":[{"package":{"ecosystem":"Packagist","name":"laravel/framework"},
				"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"11.0.0"},{"fixed":"11.31.0"}]},
				          {"type":"ECOSYSTEM","events":[{"introduced":"10.0.0"},{"fixed":"10.48.23"}]}]},
				{"package":{"ecosystem":"Packagist","name":"illuminate/routing"},
				"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"99.0.0"}]}]}]}`,
		"GHSA-bbbb": `{"id":"GHSA-bbbb","summary":"Prototype pollution","database_specific":{"severity":"CRITICAL"},
			"affected":[{"package":{"ecosystem":"npm","name":"lodash"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"4.17.21"}]}]}]}`,
		"GHSA-cccc": `{"id":"GHSA-cccc","details":"no summary, no fix yet",
			"affected":[{"package":{"ecosystem":"npm","name":"lodash"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"}]}]}]}`,
	})
	defer srv.Close()
	got, err := (&OSV{Base: srv.URL}).Check(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"dep":{"ecosystem":"Packagist","name":"laravel/framework","version":"11.9.0"},"vulns":[{"id":"GHSA-aaaa","aliases":["CVE-2026-1"],"summary":"Laravel lets a signed URL be forged","severity":"HIGH","fixed":["10.48.23","11.31.0"]}]},` +
		`{"dep":{"ecosystem":"npm","name":"lodash","version":"4.17.20"},"vulns":[{"id":"GHSA-bbbb","summary":"Prototype pollution","severity":"CRITICAL","fixed":["4.17.21"]},{"id":"GHSA-cccc","summary":"no summary, no fix yet"}]}]`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Fatalf("Check =\n%s\nwant\n%s", b, want)
	}
}

// OSV down is an error (a build says it couldn't check), never findings.
func TestCheckWhenOSVIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if got, err := (&OSV{Base: srv.URL}).Check(context.Background(), []Dep{{"npm", "a", "1.0.0"}}); err == nil || got != nil {
		t.Fatalf("Check = %v, %v; want an error", got, err)
	}
}
