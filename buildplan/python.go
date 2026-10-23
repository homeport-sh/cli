package buildplan

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Python apps (design/python.md): Django, FastAPI and Flask, installed with
// uv from uv.lock or requirements.txt. The bundle's bin is the interpreter
// itself - python-build-standalone's CPython, which links libpython
// statically - with the stdlib and the app's dependencies in lib/python3.X
// beside it. CPython finds its prefix by walking up from its executable's
// folder to lib/python3.X/os.py, so the release is a whole, relocatable
// Python wherever it's mounted: no virtualenv, nothing that records the
// build's path. The web, the release command and processes are args to it
// (-m gunicorn ..., manage.py migrate).

// PythonImage is what a Python app builds in: Ubuntu 24.04, the boxes' own
// OS and glibc (2.39), with curl, gcc and the common -dev headers for a
// dependency that builds from source. uv and CPython are fetched into the
// build by their pinned sums. Pinned by digest: a tag can be pushed again.
const PythonImage = "buildpack-deps:noble@sha256:27bdb6ab1ca0df6acfe341752ee4d9c08a341d80e3ee75ccc3b34b79af0dfaf1"

// UVVersion is the uv that installs a Python app's dependencies: its
// release's linux-gnu archive, by the sha256 the release publishes.
const UVVersion = "0.13.5"

var uvSum = map[string]string{
	"x86_64":  "f68d291c75783122dad58ab77def0e13f3233f358731db5d6d7e8715603b928f",
	"aarch64": "4bb69cf95ed13f23713bb7bc09f241896ea0c75b7c557e615a20604b82f9c403",
}

// pbsRelease is the python-build-standalone release the CPythons are from
// (github.com/astral-sh/python-build-standalone), each its
// install_only_stripped archive for linux-gnu, by the release's SHA256SUMS.
const pbsRelease = "20261021"

type pythonRelease struct {
	Version string
	Sum     map[string]string // uname -m: sha256 of the archive
}

// pythonReleases are the Pythons homeport builds with and ships, one per
// line, oldest first.
var pythonReleases = []pythonRelease{
	{"3.12.15", map[string]string{
		"x86_64":  "6d7521138fd33afddc1ccd08abb007d9a2829e38117c2576f9efdc0ec73ce772",
		"aarch64": "b4567aa5a11f51b540c4dfd9e082ebf23e554f8ac17b85caa6cea23e83693394"}},
	{"3.13.17", map[string]string{
		"x86_64":  "f829adc9d888e76c8801b4799ed444188d35239fcae3a9808a99ec5cb0d8d92c",
		"aarch64": "c6857c65b996701a5da104cbb49b35249c0c026dc0664239b04f7156aa88326a"}},
	{"3.14.9", map[string]string{
		"x86_64":  "561e91c1cadf30e84aec94d1917384ae3267e5f3e6e135e51fd1578bb19f4064",
		"aarch64": "6f77731d85a14ed848dd4041478cd3ed511523caa3d896b84b436a7edf13cf62"}},
	{"3.15.1", map[string]string{
		"x86_64":  "01d901e09ca673c99a6c9bd9ec2cfbd58dbe8813d07514c36cb73cfd20b18bd8",
		"aarch64": "6efe65c43ae0bdebebeab4607f91a0053dd36d5e51cfd69a791d544e9b122be7"}},
}

// DefaultPython is the Python an app that says none runs, and the one
// chosen when requires-python allows it: not the newest line while binary
// wheels for it are still catching up.
const DefaultPython = "3.14.9"

// PythonVersions are the Pythons offered, oldest first.
func PythonVersions() []string {
	out := make([]string, len(pythonReleases))
	for i, p := range pythonReleases {
		out[i] = p.Version
	}
	return out
}

// PythonLines are the Python lines offered, like 3.14, oldest first: what
// a Python app's settings may name.
func PythonLines() []string {
	out := make([]string, len(pythonReleases))
	for i, p := range pythonReleases {
		out[i] = pythonLine(p.Version)
	}
	return out
}

// The server installed when the app's own dependencies have none: its
// wheel, by its sha256 on PyPI. Its own dependencies resolve against what
// the app installed, so the app's locked versions stay as they are.
const (
	GunicornVersion = "26.2.0"
	gunicornSum     = "bd249d0b3f7972f7432f0a6b6ff3b3ee2d129f70cd1ff6c09a9dd9e29a2b88e3"
	UvicornVersion  = "0.54.0"
	uvicornSum      = "505bdb0f318731d45f1f712071fc781a8981f6847a31c902c9f5e652d4f67faf"
)

// pyBuild is where a Python build keeps uv, CPython and its own files, in
// the app's folder (not /tmp, a 1 GB tmpfs in the build): never shipped.
const pyBuild = ".homeport-build"

// pyEntries are the files a FastAPI or Flask app is looked for in, in order.
var pyEntries = []string{"main.py", "app.py", "wsgi.py", "asgi.py", "api.py", "server.py", "app/main.py", "app/__init__.py", "src/main.py",
	"src/app.py", "api/main.py"}

// pythonFiles are the files Python detection reads (Files).
func pythonFiles() []string {
	return slices.Concat([]string{"pyproject.toml", "uv.lock", "requirements.txt", "Pipfile", "Pipfile.lock", "poetry.lock", ".python-version",
		"runtime.txt", "manage.py", "alembic.ini"}, pyEntries)
}

// python says whether the app is a Python app: it has Python's project
// files, and no package.json unless a Python framework is recognised (a
// JavaScript app may carry a requirements.txt for a script).
func (r reader) python() bool {
	if !r.exists("pyproject.toml") && !r.exists("requirements.txt") && !r.exists("Pipfile") && !r.exists("poetry.lock") {
		return false
	}
	return !r.exists("package.json") || r.pyFramework() != ""
}

// pyFramework is the Python framework recognised, or "".
func (r reader) pyFramework() string {
	deps := r.pyDeps()
	switch {
	case deps["django"] && r.exists("manage.py"):
		return "Django"
	case deps["fastapi"]:
		return "FastAPI"
	case deps["flask"]:
		return "Flask"
	}
	return ""
}

var (
	pyNameRe    = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)
	pyNormRe    = regexp.MustCompile(`[-_.]+`)
	tomlHeadRe  = regexp.MustCompile(`(?m)^\s*\[[^\]\n]+\]\s*(#.*)?$`)
	tomlKeyRe   = func(k string) *regexp.Regexp { return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(k) + `\s*=\s*`) }
	tomlStrRe   = regexp.MustCompile(`^"([^"\n]*)"|^'([^'\n]*)'`)
	djangoMod   = regexp.MustCompile(`DJANGO_SETTINGS_MODULE["']\s*,\s*["']([A-Za-z_][A-Za-z0-9_.]*)\.settings["']`)
	pyVersionRe = regexp.MustCompile(`^(?:cpython-|python-?)?(3)\.([0-9]{1,2})(?:\.[0-9]+)?(?:[-+].*)?$`)
)

// pyName normalises a distribution's name (PEP 503).
func pyName(s string) string {
	m := pyNameRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return pyNormRe.ReplaceAllString(strings.ToLower(m[1]), "-")
}

// tomlSection is a pyproject.toml table's body, from its header to the next.
func tomlSection(b []byte, header string) string {
	s := string(b)
	for _, loc := range tomlHeadRe.FindAllStringIndex(s, -1) {
		if strings.TrimSpace(strings.SplitN(s[loc[0]:loc[1]], "#", 2)[0]) != header {
			continue
		}
		rest := s[loc[1]:]
		if next := tomlHeadRe.FindStringIndex(rest); next != nil {
			rest = rest[:next[0]]
		}
		return rest
	}
	return ""
}

// tomlString is a key's string value in a table's body.
func tomlString(section, key string) (string, bool) {
	loc := tomlKeyRe(key).FindStringIndex(section)
	if loc == nil {
		return "", false
	}
	m := tomlStrRe.FindStringSubmatch(section[loc[1]:])
	if m == nil {
		return "", false
	}
	return m[1] + m[2], true
}

// tomlStrings are a key's array of strings in a table's body.
func tomlStrings(section, key string) []string {
	loc := tomlKeyRe(key).FindStringIndex(section)
	if loc == nil {
		return nil
	}
	s := section[loc[1]:]
	if !strings.HasPrefix(s, "[") {
		return nil
	}
	var out []string
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case ']':
			return out
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '"', '\'':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return out
			}
			out = append(out, s[i+1:i+1+end])
			i += end + 1
		}
	}
	return out
}

// pyDeps are the app's dependencies, by normalised name: pyproject.toml's
// [project] dependencies and requirements.txt's.
func (r reader) pyDeps() map[string]bool {
	out := map[string]bool{}
	if b, err := r.read("pyproject.toml"); err == nil {
		for _, d := range tomlStrings(tomlSection(b, "[project]"), "dependencies") {
			if n := pyName(d); n != "" {
				out[n] = true
			}
		}
	}
	if b, err := r.read("requirements.txt"); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if line == "" || strings.HasPrefix(line, "-") {
				continue
			}
			if n := pyName(line); n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// py makes p a Python app's plan: its interpreter, its install, how it's
// served. run is a start command someone set (Settings or homeport.yaml):
// an app with nothing recognised needs one. python is the Python line the
// build settings name, if any.
func (r reader) py(p *Plan, cfg fileConfig, run, python string) error {
	install, err := r.pyInstall()
	if err != nil {
		return err
	}
	rel, why, err := r.pythonVersion(python)
	if err != nil {
		return err
	}
	framework := r.pyFramework()
	p.Toolchain, p.Image, p.Framework = "python", PythonImage, cmpOr(framework, "Python")
	p.Runtime, p.RuntimeVersion, p.RuntimeReason, p.PackageManager = "python", rel.Version, why, "uv"
	deps := r.pyDeps()

	server, web := "", ""
	switch framework {
	case "Django":
		mod, err := r.djangoProject()
		if err != nil {
			return err
		}
		if deps["uvicorn"] && !deps["gunicorn"] {
			server, web = "uvicorn", "-m uvicorn --host 0.0.0.0 --port $PORT "+mod+".asgi:application"
		} else {
			server, web = "gunicorn", "-m gunicorn --bind 0.0.0.0:$PORT "+r.gunicornConfig()+mod+".wsgi"
		}
		if !deps["whitenoise"] {
			p.Warnings = append(p.Warnings, "a Django app serves its static files (/static/) only through something like WhiteNoise: "+
				"add whitenoise to its dependencies and middleware, or they'll 404")
		}
	case "FastAPI", "Flask":
		entry, err := r.pyEntry(framework)
		switch {
		case err != nil && run == "":
			return err
		case err == nil && framework == "FastAPI":
			server, web = "uvicorn", "-m uvicorn --host 0.0.0.0 --port $PORT "+entry
		case err == nil:
			server, web = "gunicorn", "-m gunicorn --bind 0.0.0.0:$PORT "+r.gunicornConfig()+entry
		}
	default:
		if run == "" {
			return errors.New("a Python app homeport doesn't recognise (Django, FastAPI or Flask): set the start command - args to python, " +
				"like `-m myapp` or `server.py --port $PORT`")
		}
	}

	py := pyBuild + "/python/bin/python" + pythonLine(rel.Version)
	p.setup = pySetup(rel)
	p.Install = join(p.setup, install(py))
	if server != "" && cfg.Build.Command == "" {
		p.Install = join(p.Install, pyServer(py, server))
	}
	// the front-end, with the pinned Bun, before collectstatic collects it
	if assets := r.frontEnd(false); assets != "" {
		p.Install = join(p.Install, fetchBun, assets)
	}
	p.Run = web
	if framework == "Django" {
		p.build = "{ " + py + " manage.py collectstatic --noinput || echo \"homeport: built without collectstatic (manage.py collectstatic failed here:" +
			" is STATIC_ROOT set, and does the build have the variables the settings read?)\" >&2; }"
	}
	if cfg.Build.Command != "" {
		p.build = cfg.Build.Command
	}
	p.Kind = Bundle
	if cfg.Build.Artifact == "" {
		p.Artifact, p.StaticFallback = BundleDir, false
	}
	p.assemble = pyAssemble(p.Artifact, rel, server == "gunicorn" && r.gunicornConfig() != "")
	p.Command = join(p.build, p.assemble)
	return nil
}

// gunicornConf is homeport's gunicorn config, in the bundle, when the app
// has none of its own: 2 workers (the app's WEB_CONCURRENCY, if it's a
// number) of 4 threads each (gthread), as a sync worker alone serves one
// request at a time. A config file, not flags, so the app's variables win:
// gunicorn puts GUNICORN_CMD_ARGS above it. No single quotes: it's written
// in some.
var gunicornConf = []string{
	"# written by homeport: the app has no gunicorn.conf.py",
	"import os",
	"try:",
	`    workers = int(os.environ.get("WEB_CONCURRENCY", "2"))`,
	"except ValueError:",
	"    workers = 2",
	"if workers < 1:",
	"    workers = 2",
	"threads = 4",
}

const gunicornConfPath = ".homeport/gunicorn.conf.py"

// gunicornConfig is the -c that runs gunicorn with homeport's config, when
// the app has no gunicorn.conf.py (which gunicorn reads itself), with a
// trailing space; "" when it has one.
func (r reader) gunicornConfig() string {
	if r.exists("gunicorn.conf.py") {
		return ""
	}
	return "-c " + gunicornConfPath + " "
}

// pyInstall is how the app's dependencies are installed into the
// interpreter at py: uv.lock's exact versions, else requirements.txt's.
func (r reader) pyInstall() (func(py string) string, error) {
	switch {
	case r.exists("uv.lock") && r.exists("pyproject.toml"):
		return func(py string) string {
			return "{ uv export --locked --no-dev --no-editable --no-header --format requirements-txt -o " + pyBuild + "/requirements.txt --python " + py + " || " +
				"{ echo 'homeport: if uv.lock is out of date with pyproject.toml, run uv lock and commit it' >&2; exit 1; }; } && " +
				"uv pip install --python " + py + " --no-deps -r " + pyBuild + "/requirements.txt"
		}, nil
	case r.exists("requirements.txt"):
		return func(py string) string { return "uv pip install --python " + py + " -r requirements.txt" }, nil
	case r.exists("poetry.lock"):
		return nil, errors.New("poetry.lock: homeport installs Python apps with uv, which can't read Poetry's lock - " +
			"move to uv (uvx migrate-to-uv, then uv lock) and commit uv.lock, or export a requirements.txt")
	case r.exists("Pipfile"):
		return nil, errors.New("Pipfile: homeport installs Python apps with uv, which can't read Pipenv's - " +
			"export a requirements.txt (pipenv requirements > requirements.txt), or move to uv")
	}
	return nil, errors.New("pyproject.toml without uv.lock: commit the lockfile (uv lock), so the build installs what you tested - or a requirements.txt")
}

// djangoProject is the Django project's package: manage.py's
// DJANGO_SETTINGS_MODULE, less .settings.
func (r reader) djangoProject() (string, error) {
	b, _ := r.read("manage.py")
	m := djangoMod.FindSubmatch(b)
	if m == nil {
		return "", errors.New("manage.py names no DJANGO_SETTINGS_MODULE (\"<project>.settings\"): set the start command, " +
			"like `-m gunicorn --bind 0.0.0.0:$PORT <project>.wsgi`")
	}
	return string(m[1]), nil
}

// pyEntry is where a FastAPI or Flask app's application object is, as
// <module>:<name>: the first of pyEntries that makes one at its top level.
func (r reader) pyEntry(framework string) (string, error) {
	re := regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^=\n]*)?=\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?` + framework + `\(`)
	factory := false
	for _, f := range pyEntries {
		b, err := r.read(f)
		if err != nil {
			continue
		}
		if m := re.FindSubmatch(b); m != nil {
			mod := strings.TrimSuffix(strings.TrimSuffix(f, ".py"), "/__init__")
			return strings.ReplaceAll(mod, "/", ".") + ":" + string(m[1]), nil
		}
		factory = factory || strings.Contains(string(b), "def create_app")
	}
	if factory {
		return "", fmt.Errorf("a %s app made by create_app() alone: add a wsgi.py with `app = create_app()`, or set the start command"+
			" (args to python, like `-m gunicorn --bind 0.0.0.0:$PORT wsgi:app`)", framework)
	}
	return "", fmt.Errorf("no %s app found at the top level of %s: set the start command (args to python, like `-m %s`)",
		framework, strings.Join(pyEntries, ", "), map[string]string{"FastAPI": "uvicorn --host 0.0.0.0 --port $PORT main:app",
			"Flask": "gunicorn --bind 0.0.0.0:$PORT app:app"}[framework])
}

// pythonLine is a version's major.minor.
func pythonLine(v string) string {
	f := strings.SplitN(v, ".", 3)
	return f[0] + "." + f[1]
}

// pythonVersion is the Python the app runs, and why: the build settings'
// line (set), else .python-version's line, runtime.txt's, the default if
// requires-python allows it, else the newest it allows.
func (r reader) pythonVersion(set string) (pythonRelease, string, error) {
	if set != "" { // Settings.Check took it: one of PythonLines
		for _, p := range pythonReleases {
			if pythonLine(p.Version) == set {
				return p, "the build settings say " + set, nil
			}
		}
	}
	lines := make([]string, len(pythonReleases))
	for i, p := range pythonReleases {
		lines[i] = pythonLine(p.Version)
	}
	offered := " (homeport runs " + strings.Join(lines, ", ") + ")"
	for _, f := range []string{".python-version", "runtime.txt"} {
		v := r.firstLine(f)
		if v == "" {
			continue
		}
		m := pyVersionRe.FindStringSubmatch(v)
		if m == nil {
			return pythonRelease{}, "", fmt.Errorf("%s says %q, which isn't a CPython 3 version%s", f, v, offered)
		}
		line := m[1] + "." + m[2]
		for _, p := range pythonReleases {
			if pythonLine(p.Version) == line {
				return p, f + " says " + v, nil
			}
		}
		return pythonRelease{}, "", fmt.Errorf("%s asks for Python %s%s", f, line, offered)
	}
	b, _ := r.read("pyproject.toml")
	spec, ok := tomlString(tomlSection(b, "[project]"), "requires-python")
	def := pythonReleases[slices.IndexFunc(pythonReleases, func(p pythonRelease) bool { return p.Version == DefaultPython })]
	if !ok || strings.TrimSpace(spec) == "" {
		return def, "the default: nothing names a Python", nil
	}
	allows, err := pep440(spec)
	if err != nil {
		return def, "the default: requires-python " + strconv.Quote(spec) + " isn't one homeport reads", nil
	}
	if allows(def.Version) {
		return def, "the default, which requires-python " + strconv.Quote(spec) + " allows", nil
	}
	for _, p := range slices.Backward(pythonReleases) {
		if allows(p.Version) {
			return p, "the newest that requires-python " + strconv.Quote(spec) + " allows", nil
		}
	}
	return pythonRelease{}, "", fmt.Errorf("pyproject.toml's requires-python %q allows none of the Pythons homeport runs%s", spec, offered)
}

var pep440Re = regexp.MustCompile(`^(~=|==|!=|<=|>=|<|>)\s*v?([0-9]+(?:\.[0-9]+)*)(\.\*)?$`)

// pep440 reads a version specifier (PEP 440, the forms requires-python
// uses): what it allows of major.minor.patch versions.
func pep440(spec string) (func(string) bool, error) {
	var checks []func([]int) bool
	for c := range strings.SplitSeq(spec, ",") {
		m := pep440Re.FindStringSubmatch(strings.TrimSpace(c))
		if m == nil {
			return nil, fmt.Errorf("%q", c)
		}
		op, want, star := m[1], pyInts(m[2]), m[3] != ""
		prefix := func(v []int, w []int) bool { return slices.Equal(v[:min(len(w), len(v))], w) }
		var check func([]int) bool
		switch {
		case star && op == "==":
			check = func(v []int) bool { return prefix(v, want) }
		case star && op == "!=":
			check = func(v []int) bool { return !prefix(v, want) }
		case star:
			return nil, fmt.Errorf("%q", c)
		case op == "~=":
			if len(want) < 2 {
				return nil, fmt.Errorf("%q", c)
			}
			stem := want[:len(want)-1]
			check = func(v []int) bool { return pyCmp(v, want) >= 0 && prefix(v, stem) }
		default:
			check = func(v []int) bool {
				c := pyCmp(v, want)
				return map[string]bool{"==": c == 0, "!=": c != 0, "<=": c <= 0, ">=": c >= 0, "<": c < 0, ">": c > 0}[op]
			}
		}
		checks = append(checks, check)
	}
	return func(v string) bool {
		x := pyInts(v)
		for _, c := range checks {
			if !c(x) {
				return false
			}
		}
		return true
	}, nil
}

func pyInts(v string) []int {
	var out []int
	for f := range strings.SplitSeq(v, ".") {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}

// pyCmp compares versions, the shorter padded with zeros.
func pyCmp(a, b []int) int {
	for i := range max(len(a), len(b)) {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// pySetup fetches uv and CPython into pyBuild, each by its pinned sum,
// keeping the archives in the build's home (the app's build cache) for the
// next build; and points uv at that CPython. A person's install keeps it.
func pySetup(rel pythonRelease) string {
	arch := func(a string) string {
		return a + ") a=" + a + " u=" + uvSum[a] + " p=" + rel.Sum[a] + " ;;"
	}
	// fetch: the archive at $f, from url, unless what's cached is the one
	fetch := func(f, sum, url string) string {
		return "f=\"$HOME/.cache/homeport/" + f + "\" && { echo \"" + sum + "  $f\" | sha256sum -c --quiet - 2>/dev/null || " +
			"{ curl -fsSL -o \"$f\" " + url + " && echo \"" + sum + "  $f\" | sha256sum -c --quiet -; }; }"
	}
	py := "cpython-" + rel.Version + "%2B" + pbsRelease + "-$a-unknown-linux-gnu-install_only_stripped.tar.gz"
	return join(
		"case $(uname -m) in "+arch("x86_64")+" "+arch("aarch64")+" *) echo \"homeport: no Python for $(uname -m)\" >&2; exit 1 ;; esac",
		"rm -rf "+pyBuild+" && mkdir -p "+pyBuild+" \"$HOME/.cache/homeport\"",
		fetch("uv-"+UVVersion+"-$a.tar.gz", "$u", "https://github.com/astral-sh/uv/releases/download/"+UVVersion+"/uv-$a-unknown-linux-gnu.tar.gz"),
		"tar -xzf \"$f\" -C "+pyBuild+" --strip-components=1 uv-$a-unknown-linux-gnu/uv",
		fetch("cpython-"+rel.Version+"-"+pbsRelease+"-$a.tar.gz", "$p", "https://github.com/astral-sh/python-build-standalone/releases/download/"+pbsRelease+"/"+py),
		"tar -xzf \"$f\" -C "+pyBuild,
		"export PATH=\"$PWD/"+pyBuild+":$PATH\" UV_PYTHON_DOWNLOADS=never UV_LINK_MODE=copy UV_NO_PROGRESS=1")
}

// pyServer installs the server the plan runs, pinned, when the app's own
// dependencies didn't.
func pyServer(py, server string) string {
	v, sum := GunicornVersion, gunicornSum
	if server == "uvicorn" {
		v, sum = UvicornVersion, uvicornSum
	}
	req := server + "==" + v + " --hash=sha256:" + sum
	return "{ " + py + " -c 'import " + server + "' 2>/dev/null || { echo \"homeport: " + server + " isn't in the app's dependencies: installing " +
		server + " " + v + "\" >&2 && echo '" + req + "' > " + pyBuild + "/server.txt && uv pip install --python " + py + " -r " + pyBuild + "/server.txt; }; }"
}

// pyAssemble makes the bundle b: the app's files; CPython's stdlib and the
// installed dependencies under lib/python3.X, without what a server never
// runs (IDLE, Tk, ensurepip, pip); the interpreter as bin; and everything
// compiled, since a release is read-only and Python could write no
// __pycache__ of its own (unchecked-hash: no source stat on import either).
// conf: with homeport's gunicorn config (gunicornConf), compiled with the
// rest.
func pyAssemble(b string, rel pythonRelease, conf bool) string {
	line := pythonLine(rel.Version)
	lib := b + "/lib/python" + line
	writeConf := ""
	if conf {
		writeConf = "printf '%s\\n' '" + strings.Join(gunicornConf, "' '") + "' > " + b + "/" + gunicornConfPath
	}
	return join(
		"{ [ ! -d bin ] || { echo 'homeport: the app has a bin/ folder, and a Python bundle'\"'\"'s bin is its interpreter: rename the folder' >&2; exit 1; }; }",
		"rm -rf "+b+" && mkdir -p "+b+"/.homeport",
		copyTree("r=$(pwd -P)", ".", b, "./.git", "./"+b, "./"+pyBuild, "./.venv", "./venv", "./node_modules", "./.mypy_cache", "./.pytest_cache",
			"./.ruff_cache"),
		"find "+b+" -name __pycache__ -type d -prune -exec rm -rf {} +",
		"{ [ ! -e "+lib+" ] || { echo 'homeport: the app has a lib/python"+line+" folder, where its bundle'\"'\"'s Python goes: rename it' >&2; exit 1; }; }",
		"mkdir -p "+b+"/lib && cp -R "+pyBuild+"/python/lib/python"+line+" "+b+"/lib/",
		"rm -rf "+path.Join(lib, "idlelib")+" "+path.Join(lib, "tkinter")+" "+path.Join(lib, "turtledemo")+" "+path.Join(lib, "ensurepip")+" "+
			lib+"/site-packages/pip "+lib+"/site-packages/pip-*.dist-info",
		"cp "+pyBuild+"/python/bin/python"+line+" "+b+"/bin",
		writeConf,
		"{ "+b+"/bin -m compileall -q -j 0 --invalidation-mode unchecked-hash "+b+" || echo 'homeport: the files above don'\"'\"'t compile: Python says so if they'\"'\"'re imported' >&2; }")
}

// pyRelease: a Django app's migrations, or Alembic's (FastAPI's, Flask's
// through Flask-Migrate), as args to the interpreter.
func (r reader) pyRelease(p *Plan) (release, why, warning string) {
	switch {
	case p.Framework == "Django":
		return "manage.py migrate --noinput", "a Django app: its migrations (manage.py migrate --noinput)", ""
	case r.exists("alembic.ini") && r.pyDeps()["alembic"]:
		return "-m alembic upgrade head", "Alembic's migrations (alembic.ini): alembic upgrade head", ""
	}
	return "", "", ""
}
