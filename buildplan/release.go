package buildplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A release command, when nothing said one: the obvious migration of the
// app's framework, run as args to its bin before each release goes live
// (sandbox.go), whose failure stops the deploy. Only what runs as it is in
// what ships: anything else is said in the plan's warnings, for a person to
// set. homeport.yaml's release: "" says the app has none.

// migrateScripts are package.json scripts that migrate, in the order read.
var migrateScripts = []string{"migrate", "db:migrate"}

// rscKitMigrate: rsc-kit runs src/commands/<name>.* as ./app <name>.
var rscKitMigrate = RSCKitCommandFiles("migrate")

// rscKitExts are a command file's extensions, in the order rsc-kit reads.
var rscKitExts = []string{"ts", "mts", "cts", "tsx", "js", "mjs", "cjs", "jsx"}

// RSCKitCommandFiles are the files that would be each named command.
func RSCKitCommandFiles(names ...string) []string {
	var out []string
	for _, n := range names {
		for _, e := range rscKitExts {
			out = append(out, "src/commands/"+n+"."+e)
		}
	}
	return out
}

// prismaLock is in Prisma's migrations folder once it has any.
const prismaLock = "prisma/migrations/migration_lock.toml"

// migrateBins: where a migration tool's command is in its package, to run
// it by file (a bundle has no node_modules/.bin).
var migrateBins = map[string]string{"prisma": "build/index.js", "drizzle-kit": "bin.cjs"}

// releaseFiles are the files release detection reads (Files).
func releaseFiles() []string { return append(slices.Clone(rscKitMigrate), prismaLock) }

// detectRelease: the release command p's framework migrates with, and why;
// or a warning saying why there's none it could run.
func (r reader) detectRelease(p *Plan) (release, why, warning string) {
	switch {
	case p.Toolchain == "php" && p.Kind == Bundle && r.requires("laravel/framework"):
		return "php-cli artisan migrate --force", "a Laravel app: its migrations (php artisan migrate --force)", ""
	case p.Toolchain == "python" && p.Kind == Bundle:
		return r.pyRelease(p)
	case p.rscKitApp:
		for _, f := range rscKitMigrate {
			if r.exists(f) {
				return "migrate", "rsc-kit runs " + f + " as ./app migrate", ""
			}
		}
		return "", "", ""
	case p.Kind != Bundle || (p.Toolchain != "node" && p.Toolchain != "bun"):
		return "", "", ""
	}
	pkg := r.pkg()
	for _, name := range migrateScripts {
		if pkg.Scripts[name] != "" {
			return r.scriptArgs(p, pkg, name, releaseUse)
		}
	}
	prod := r.prodDeps()
	if r.exists(prismaLock) {
		if !prod["prisma"] {
			return "", "", "it has Prisma's migrations (" + prismaLock + "), but prisma is in devDependencies, and the bundle ships dependencies only: " +
				"move it to dependencies for the release command to run prisma migrate deploy, or set one"
		}
		return "node_modules/prisma/" + migrateBins["prisma"] + " migrate deploy", "it has Prisma's migrations (prisma/migrations): prisma migrate deploy", ""
	}
	return "", "", ""
}

// scriptUse is what a script is run as: what it's called (a release
// command, a queue drain), a file a person might give it, and the packages
// whose command it may run by its file (the file in the package).
type scriptUse struct {
	what, example string
	bins          map[string]string
}

var releaseUse = scriptUse{"release command", "migrate.js", migrateBins}

// scriptArgs: package.json's script name as args to p's runtime, run as
// u - the file it runs, with the runtime's flags and the file's args, or a
// package's command by its file - and where they came from; or a warning
// saying why it can't run as one.
func (r reader) scriptArgs(p *Plan, pkg pkgJSON, name string, u scriptUse) (args, from, warning string) {
	what, bins := u.what, u.bins
	from = fmt.Sprintf("its %q script (%s)", name, pkg.Scripts[name])
	st, err := parseStart(pkg.Scripts, name, 0)
	if err != nil {
		says := " can't run as a " + what
		if errors.Is(err, errShell) || errors.Is(err, errEnv) {
			says = " " + err.Error() + ", which a " + what + " can't"
		}
		return "", "", from + says + ": set the " + what + ", args to " + p.Runtime + " (like `" + u.example + "`)"
	}
	if (st.bin == "bunx" || st.bin == "npx") && len(st.args) > 0 {
		args := slices.DeleteFunc(slices.Clone(st.args), func(a string) bool { return a == "--bun" || a == "-y" || a == "--yes" })
		if len(args) > 0 {
			st.bin, st.args = args[0], args[1:]
		}
	}
	switch {
	case st.file != "" && st.runtime != "" && st.runtime != p.Runtime:
		return "", "", fmt.Sprintf("%s runs on %s, and the app on %s: set the %s", from, title(st.runtime), title(p.Runtime), what)
	case st.file != "":
		return strings.Join(slices.Concat(st.flags, []string{st.file}, st.args), " "), from, ""
	case bins[st.bin] == "":
		return "", "", from + " runs " + st.bin + ": set the " + what + " to its file under node_modules, as args to " + p.Runtime
	case !r.prodDeps()[st.bin]:
		return "", "", fmt.Sprintf("%s runs %s, which is in devDependencies, and the bundle ships dependencies only: move it to dependencies for the %s to run it, or set one", from, st.bin, what)
	}
	return strings.Join(slices.Concat([]string{"node_modules/" + st.bin + "/" + bins[st.bin]}, st.args), " "), from, ""
}

// prodDeps: package.json's dependencies alone, what a bundle ships.
func (r reader) prodDeps() map[string]bool {
	var pkg struct {
		Deps map[string]any `json:"dependencies"`
	}
	b, _ := r.read("package.json")
	_ = json.Unmarshal(b, &pkg)
	out := map[string]bool{}
	for k := range pkg.Deps {
		out[k] = true
	}
	return out
}
