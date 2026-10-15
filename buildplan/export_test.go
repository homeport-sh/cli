package buildplan

// WithCatalog makes c the curated packages until the returned func runs:
// a published package for the tests, before any real one is.
func WithCatalog(c []Package) (restore func()) {
	old := catalog
	catalog = c
	return func() { catalog = old }
}
