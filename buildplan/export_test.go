package buildplan

import (
	"encoding/base64"
	"encoding/json"
)

// WithCatalog makes c the curated packages until the returned func runs:
// a published package for the tests, before any real one is.
func WithCatalog(c []Package) (restore func()) {
	old := catalog
	catalog = c
	return func() { catalog = old }
}

// APIBuildArg is the generator's argument for p's api/ folder (api.go),
// building into out, with a body limit (bytes) and a time to the first
// byte (ms) of the test's.
func APIBuildArg(p Plan, hasPkg bool, out string, limit, timeout int) string {
	c := newAPIConfig(&apiScan{routes: p.API.Routes, middleware: p.API.Middleware}, p.API.Frontend, p.Runtime, hasPkg)
	c.Out, c.Limit, c.Timeout = out, limit, timeout
	j, _ := json.Marshal(c)
	return base64.StdEncoding.EncodeToString(j)
}
