package buildplan

import "testing"

func TestCurrentImageKeepsARuntimeLinePatched(t *testing.T) {
	old24 := "node:24.20.0-bookworm@sha256:aaaa"
	for _, c := range []struct {
		name, built, want, line string
		ok                      bool
	}{
		{"an older Node 24 gets today's 24", old24, nodeImage("24.21.0"), "node 24", true},
		{"today's Node 22 is current", nodeImage("22.23.3"), nodeImage("22.23.3"), "node 22", true},
		{"a Node major homeport stopped pinning isn't kept", "node:20.19.0-bookworm@sha256:bbbb", "", "", false},
		{"an older Bun gets today's", "oven/bun:1.4.3@sha256:cccc", BunImage, "bun", true},
		{"an older FrankenPHP gets today's PHP", "ghcr.io/homeport-sh/frankenphp:8.5-1.13.5-bun1.4.4@sha256:dddd", FrankenPHPImage, "php", true},
		{"Go's version is the project's (go.mod)", "golang:1.25.3", "", "", false},
		{"a project's own image is the project's", "ghcr.io/acme/builder:1", "", "", false},
		{"a Node image not pinned by digest isn't homeport's", "node:24.20.0", "", "", false},
		{"nothing built, nothing to keep", "", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, line, ok := CurrentImage(c.built)
			if got != c.want || line != c.line || ok != c.ok {
				t.Fatalf("CurrentImage(%q) = %q, %q, %v; want %q, %q, %v", c.built, got, line, ok, c.want, c.line, c.ok)
			}
		})
	}
}
