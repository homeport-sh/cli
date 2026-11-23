package buildplan

import "strings"

// CurrentImage is the image a build would take today for the runtime line
// built was built with: the pinned release of the same Node major, Bun's,
// or homeport's FrankenPHP (PHP). A control plane that kept the image each
// release was built with rebuilds a live app whose image isn't current, so
// a runtime's security patch reaches it without its owner pushing anything.
//
// ok is false where the line isn't homeport's to keep current: a project's
// own image, Go's version (go.mod's), a Node major homeport no longer pins,
// or anything not pinned by digest the way homeport pins its own.
func CurrentImage(built string) (current, line string, ok bool) {
	name, _, pinned := strings.Cut(built, "@sha256:")
	if !pinned {
		return "", "", false
	}
	switch {
	case strings.HasPrefix(name, "node:"):
		v, _, _ := strings.Cut(strings.TrimPrefix(name, "node:"), "-")
		major, _, _ := strings.Cut(v, ".")
		for _, n := range nodeReleases {
			if m, _, _ := strings.Cut(n.Version, "."); m == major {
				return nodeImage(n.Version), "node " + major, true
			}
		}
	case strings.HasPrefix(name, "oven/bun:"):
		return BunImage, "bun", true
	case strings.HasPrefix(name, "ghcr.io/homeport-sh/frankenphp:"):
		return FrankenPHPImage, "php", true
	}
	return "", "", false
}
