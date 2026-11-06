package buildplan

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// A framework's fingerprinted files - their folder after the build, and
// the URL path the app answers them at - are named in the plan (Assets),
// for the builder to keep with the release: the release after it answers
// a tab opened before that deploy with them, when the app 404s one. Every
// framework whose folder the build knows names it, whatever the bundle
// has besides (a tree in .homeport/public, an embedded copy).

var (
	// the builder's own rules (homeportd's PlanAssets): a plain folder of
	// the app's, a plain URL path below /. A plan with one that isn't is
	// refused, so one is left out here instead.
	assetFromRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	assetAtRe   = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

	// SvelteKit's kit options: in svelte.config.js, or (SvelteKit 3) the
	// sveltekit() plugin's in the Vite config
	sveltekitCallRe = regexp.MustCompile(`\bsveltekit\s*\(`)
	kitOutDirRe     = regexp.MustCompile(`\boutDir\s*:\s*['"]([^'"]+)['"]`)
	kitAppDirRe     = regexp.MustCompile(`\bappDir\s*:\s*['"]([^'"]+)['"]`)
	kitBaseRe       = regexp.MustCompile(`\bpaths\s*:\s*\{[^}]*\bbase\s*:\s*['"]([^'"]*)['"]`)

	// laravel-vite-plugin's options
	laravelCallRe     = regexp.MustCompile(`\blaravel\s*\(`)
	laravelBuildDirRe = regexp.MustCompile(`\bbuildDirectory\s*:\s*['"]([^'"]+)['"]`)
	laravelPublicRe   = regexp.MustCompile(`\bpublicDirectory\s*:\s*['"]([^'"]+)['"]`)

	// Django's settings: STATIC_ROOT under BASE_DIR, and STATIC_URL
	staticRootRe = regexp.MustCompile(`(?m)^STATIC_ROOT\s*=\s*(?:str\(\s*)?(?:BASE_DIR\s*/\s*|os\.path\.join\(\s*BASE_DIR\s*,\s*)['"]([^'"]+)['"]`)
	staticURLRe  = regexp.MustCompile(`(?m)^STATIC_URL\s*=\s*['"]([^'"]*)['"]`)
)

// planAssets is the entries of from, at pairs, each cleaned, leaving out
// one the builder wouldn't take; nil without any.
func planAssets(pairs ...string) []PlanAsset {
	var out []PlanAsset
	for i := 0; i+1 < len(pairs); i += 2 {
		from, at := pairs[i], pairs[i+1]
		if !relPath(from) || strings.HasPrefix(from, "/") {
			continue
		}
		from, at = clean(from), path.Clean("/"+at)
		if !assetFromRe.MatchString(from) || slices.Contains(strings.Split(from, "/"), "..") || from == ".homeport" || strings.HasPrefix(from, ".homeport/") ||
			!assetAtRe.MatchString(at) {
			continue
		}
		out = append(out, PlanAsset{From: from, At: at})
	}
	return out
}

// frameworkAssets: the fingerprinted files of the framework's build (pr's),
// compiled or not; none for a framework without a folder the build knows.
func (r reader) frameworkAssets(pr *preset, compiled bool) []PlanAsset {
	if pr == nil {
		return nil
	}
	switch pr.name {
	case "SvelteKit":
		return r.svelteKitAssets(pr, compiled)
	case "Astro":
		return planAssets("dist/client/_astro", "/_astro")
	case "React Router":
		return planAssets("build/client/assets", "/assets")
	case "Remix":
		// Vite's build, else the classic compiler's assetsBuildDirectory
		vite := strings.Contains(r.pkg().Scripts[r.buildScript()], "vite:build") || slices.ContainsFunc(viteConfigs, r.exists)
		if vite {
			return planAssets("build/client/assets", "/assets")
		}
		return planAssets("public/build", "/build")
	case "Next.js":
		return planAssets(".next/static", "/_next/static")
	case "Nuxt":
		return planAssets(".output/public/_nuxt", "/_nuxt")
	}
	if pr.layout == "output" {
		// another Nitro build (TanStack Start's)
		return planAssets(".output/public/assets", "/assets")
	}
	return nil
}

// svelteKitAssets: <appDir>/immutable, at paths.base. A compiled adapter-bun
// server embeds them, and leaves them in the kit's outDir only; else
// they're in the adapter's out.
func (r reader) svelteKitAssets(pr *preset, compiled bool) []PlanAsset {
	opts := r.svelteKitOptions()
	str := func(re *regexp.Regexp, def string) string {
		if m := re.FindSubmatch(opts); m != nil {
			return string(m[1])
		}
		return def
	}
	appDir, base := str(kitAppDirRe, "_app"), str(kitBaseRe, "")
	from := str(kitOutDirRe, ".svelte-kit") + "/output/client"
	if !compiled || !pr.only {
		from = "build"
		if m := adapterOutRe.FindSubmatch(r.svelteAdapterCall(pr)); m != nil {
			from = string(m[1])
		}
		from += "/client"
	}
	return planAssets(from+"/"+appDir+"/immutable", base+"/"+appDir+"/immutable")
}

// svelteKitOptions: the text of svelte.config.js, and of the sveltekit()
// plugin's options in a Vite config, comments stripped.
func (r reader) svelteKitOptions() []byte {
	var out []byte
	for _, f := range svelteConfigs {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		if slices.Contains(viteConfigs, f) {
			out = append(out, callText(b, sveltekitCallRe)...)
		} else {
			out = append(out, stripComments(b)...)
		}
		out = append(out, '\n')
	}
	return out
}

// laravelAssets: what laravel-vite-plugin builds, when the app builds its
// front end with it: <publicDirectory>/<buildDirectory>/assets.
func (r reader) laravelAssets() []PlanAsset {
	if !r.deps()["laravel-vite-plugin"] || !r.hasScript("build") {
		return nil
	}
	public, build := "public", "build"
	for _, f := range viteConfigs {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		opts := callText(b, laravelCallRe)
		if m := laravelBuildDirRe.FindSubmatch(opts); m != nil {
			build = string(m[1])
		}
		if m := laravelPublicRe.FindSubmatch(opts); m != nil {
			public = string(m[1])
		}
		break
	}
	return planAssets(public+"/"+build+"/assets", build+"/assets")
}

// djangoAssets: what collectstatic collects, when the settings put
// STATIC_ROOT under BASE_DIR (the app's folder), at STATIC_URL - none when
// that's another host's.
func (r reader) djangoAssets() []PlanAsset {
	mod, err := r.djangoProject()
	if err != nil {
		return nil
	}
	dir := strings.ReplaceAll(mod, ".", "/")
	for _, f := range []string{dir + "/settings.py", dir + "/settings/__init__.py", dir + "/settings/base.py"} {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		root := staticRootRe.FindSubmatch(b)
		if root == nil {
			continue
		}
		at := "/static"
		if m := staticURLRe.FindSubmatch(b); m != nil {
			if strings.Contains(string(m[1]), "//") {
				return nil
			}
			at = "/" + strings.Trim(string(m[1]), "/")
		}
		return planAssets(string(root[1]), at)
	}
	return nil
}
