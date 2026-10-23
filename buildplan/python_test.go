package buildplan_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// pyproject is a uv project's files: pyproject.toml depending on deps, its
// uv.lock, and the extra files given in pairs.
func pyproject(deps []string, files ...string) map[string]string {
	quoted := make([]string, len(deps))
	for i, d := range deps {
		quoted[i] = `  "` + d + `",`
	}
	m := map[string]string{
		"pyproject.toml": "[project]\nname = \"app\"\nversion = \"0.1.0\"\nrequires-python = \">=3.12\"\ndependencies = [\n" + strings.Join(quoted, "\n") + "\n]\n",
		"uv.lock":        "version = 1\n",
	}
	for i := 0; i+1 < len(files); i += 2 {
		m[files[i]] = files[i+1]
	}
	return m
}

const managePy = `#!/usr/bin/env python
import os, sys

def main():
    os.environ.setdefault("DJANGO_SETTINGS_MODULE", "mysite.settings")
    from django.core.management import execute_from_command_line
    execute_from_command_line(sys.argv)
`

// A Python app ships as a bundle whose bin is the interpreter itself
// (python-build-standalone's, static), with the stdlib and the app's deps in
// lib/python3.X beside it: CPython finds its prefix from bin's folder, so the
// release is a relocatable Python, with no virtualenv.
func TestAPythonAppShipsAsABundleWithItsInterpreter(t *testing.T) {
	p := detect(t, pyproject([]string{"flask>=3.1", "gunicorn>=23"}, "app.py", "from flask import Flask\n\napp = Flask(__name__)\n"), buildplan.Settings{})
	if p.Kind != buildplan.Bundle || p.Toolchain != "python" || p.Framework != "Flask" || p.Artifact != buildplan.BundleDir {
		t.Fatalf("plan: %+v", p)
	}
	if p.Image != buildplan.PythonImage || !strings.Contains(p.Image, "@sha256:") {
		t.Errorf("image: %s", p.Image)
	}
	if p.Runtime != "python" || p.RuntimeVersion != buildplan.DefaultPython || p.PackageManager != "uv" {
		t.Errorf("runtime: %s %s %s", p.Runtime, p.RuntimeVersion, p.PackageManager)
	}
	if p.Run != "-m gunicorn --bind 0.0.0.0:$PORT --workers 2 --threads 4 app:app" {
		t.Errorf("run: %s", p.Run)
	}
	if err := buildplan.CheckRun(p.Run); err != nil {
		t.Errorf("run isn't one the sandbox takes: %v", err)
	}
	// uv and CPython by their pinned sums, cached in the build's home; the
	// lock's exact versions, installed into that CPython
	for _, want := range []string{"sha256sum -c", "$HOME/.cache/homeport", "python-build-standalone/releases/download/",
		"install_only_stripped.tar.gz", "uv export --locked --no-dev --no-editable", "run uv lock", "uv pip install", "--no-deps"} {
		if !strings.Contains(p.Install, want) {
			t.Errorf("install has no %q: %s", want, p.Install)
		}
	}
	// the bundle: the app's files, the stdlib and deps under lib/, the
	// interpreter as bin, everything compiled for a read-only release
	for _, want := range []string{"rm -rf .homeport-bundle", "--exclude=./.git", "--exclude=./.venv", "--exclude=./.homeport-build",
		"lib/python3.14", "/bin/python3.14 .homeport-bundle/bin", "compileall", "unchecked-hash", "links outside the app"} {
		if !strings.Contains(p.Command, want) {
			t.Errorf("command has no %q: %s", want, p.Command)
		}
	}
	if p.Build != "" {
		t.Errorf("a Flask app has no build of its own: %q", p.Build)
	}
}

// requirements.txt works without pyproject.toml; pyproject.toml without a
// lock doesn't (as composer.json without composer.lock doesn't).
func TestPythonsLockfiles(t *testing.T) {
	p := detect(t, map[string]string{"requirements.txt": "fastapi==0.120.0\nuvicorn==0.54.0\n", "main.py": "from fastapi import FastAPI\napp = FastAPI()\n"},
		buildplan.Settings{})
	if p.Framework != "FastAPI" || !strings.Contains(p.Install, "uv pip install") || !strings.Contains(p.Install, "-r requirements.txt") ||
		strings.Contains(p.Install, "uv export") {
		t.Errorf("requirements.txt: %+v", p)
	}
	for name, files := range map[string]map[string]string{
		"uv.lock": {"pyproject.toml": "[project]\nname = \"x\"\ndependencies = [\"flask\"]\n", "app.py": "app = Flask(__name__)"},
		"Pipfile": {"Pipfile": "[packages]\nflask = \"*\"\n", "Pipfile.lock": "{}"},
		"uv lock": {"pyproject.toml": "[tool.poetry]\nname = \"x\"\n", "poetry.lock": ""},
	} {
		_, err := buildplan.Detect(repo(files), buildplan.Settings{})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: want an error naming %s, got %v", files, name, err)
		}
	}
}

// Each framework: how it's recognised, its entry, how it's served, and the
// release command detected.
func TestPythonFrameworks(t *testing.T) {
	for _, tc := range []struct {
		name               string
		files              map[string]string
		framework, run     string
		release, releaseOf string
	}{
		{"django on gunicorn", pyproject([]string{"Django>=5.2", "gunicorn"}, "manage.py", managePy),
			"Django", "-m gunicorn --bind 0.0.0.0:$PORT --workers 2 --threads 4 mysite.wsgi", "manage.py migrate --noinput", "Django"},
		// uvicorn and no gunicorn: the ASGI app
		{"django on uvicorn", pyproject([]string{"django", "uvicorn[standard]"}, "manage.py", managePy),
			"Django", "-m uvicorn --host 0.0.0.0 --port $PORT mysite.asgi:application", "manage.py migrate --noinput", "Django"},
		{"fastapi in app/main.py", pyproject([]string{"fastapi[standard]>=0.120"}, "app/main.py", "import fastapi\n\napi: fastapi.FastAPI = fastapi.FastAPI(title=\"x\")\n"),
			"FastAPI", "-m uvicorn --host 0.0.0.0 --port $PORT app.main:api", "", ""},
		{"fastapi with alembic", pyproject([]string{"fastapi", "alembic", "sqlalchemy"}, "main.py", "app = FastAPI()\n", "alembic.ini", "[alembic]\n"),
			"FastAPI", "-m uvicorn --host 0.0.0.0 --port $PORT main:app", "-m alembic upgrade head", "Alembic"},
		{"flask in wsgi.py", map[string]string{"requirements.txt": "Flask==3.1.2\n", "wsgi.py": "from myapp import create_app\napplication = create_app()\nserver = Flask('x')\n"},
			"Flask", "-m gunicorn --bind 0.0.0.0:$PORT --workers 2 --threads 4 wsgi:server", "", ""},
	} {
		p := detect(t, tc.files, buildplan.Settings{})
		if p.Framework != tc.framework || p.Run != tc.run || p.Release != tc.release {
			t.Errorf("%s: framework %q run %q release %q", tc.name, p.Framework, p.Run, p.Release)
		}
		if tc.releaseOf != "" && !strings.Contains(p.ReleaseReason, tc.releaseOf) {
			t.Errorf("%s: release reason %q", tc.name, p.ReleaseReason)
		}
		if err := buildplan.CheckRelease(p.Release); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Django: its static files collected at build, without failing a build whose
// settings need what the build doesn't have; a warning when nothing will
// serve them.
func TestDjangoCollectsItsStaticFiles(t *testing.T) {
	p := detect(t, pyproject([]string{"django", "gunicorn"}, "manage.py", managePy), buildplan.Settings{})
	if !strings.Contains(p.Command, "manage.py collectstatic --noinput ||") {
		t.Errorf("command: %s", p.Command)
	}
	if !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "whitenoise") }) {
		t.Errorf("warnings: %q", p.Warnings)
	}
	p = detect(t, pyproject([]string{"django", "gunicorn", "whitenoise[brotli]"}, "manage.py", managePy), buildplan.Settings{})
	if slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "whitenoise") }) {
		t.Errorf("whitenoise is there: %q", p.Warnings)
	}
}

// The server the plan names is installed, pinned, when the app's own deps
// don't have it; the app's locked versions are never touched.
func TestTheServerIsInstalledWhenTheAppHasNone(t *testing.T) {
	p := detect(t, pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n"), buildplan.Settings{})
	if !strings.Contains(p.Install, "import gunicorn") || !strings.Contains(p.Install, "gunicorn=="+buildplan.GunicornVersion+" --hash=sha256:") {
		t.Errorf("install: %s", p.Install)
	}
	p = detect(t, pyproject([]string{"fastapi"}, "main.py", "app = FastAPI()\n"), buildplan.Settings{})
	if !strings.Contains(p.Install, "import uvicorn") || !strings.Contains(p.Install, "uvicorn=="+buildplan.UvicornVersion+" --hash=sha256:") {
		t.Errorf("install: %s", p.Install)
	}
}

// Nothing recognised: a Python app with a start command someone set runs it;
// one with none says to set it. A Flask factory alone can't be started
// without parentheses, which a start command can't hold.
func TestAPythonAppWithNoFrameworkNeedsAStartCommand(t *testing.T) {
	plain := pyproject([]string{"httpx"}, "server.py", "print('hi')\n")
	if _, err := buildplan.Detect(repo(plain), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "start command") {
		t.Errorf("no start command: %v", err)
	}
	p := detect(t, plain, buildplan.Settings{Run: "server.py --port $PORT"})
	if p.Framework != "Python" || p.Run != "server.py --port $PORT" {
		t.Errorf("plan: %+v", p)
	}
	plain["homeport.yaml"] = "run: -m myserver\n"
	if p := detect(t, plain, buildplan.Settings{}); p.Run != "-m myserver" {
		t.Errorf("homeport.yaml's run: %q", p.Run)
	}
	factory := pyproject([]string{"flask"}, "app/__init__.py", "def create_app():\n    app = Flask(__name__)\n    return app\n")
	if _, err := buildplan.Detect(repo(factory), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "create_app") {
		t.Errorf("factory: %v", err)
	}
}

// Which Python: .python-version, runtime.txt, requires-python, else the
// default; always homeport's pinned patch of the line.
func TestWhichPython(t *testing.T) {
	line := func(v string) string { return strings.Join(strings.Split(v, ".")[:2], ".") }
	for _, tc := range []struct {
		name, file, body, want string
	}{
		{"python-version", ".python-version", "3.13\n", "3.13"},
		{"python-version patch", ".python-version", "3.12.4\n", "3.12"},
		{"python-version cpython", ".python-version", "cpython-3.15\n", "3.15"},
		{"runtime.txt", "runtime.txt", "python-3.13.1\n", "3.13"},
	} {
		files := pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n", tc.file, tc.body)
		p := detect(t, files, buildplan.Settings{})
		if line(p.RuntimeVersion) != tc.want || !strings.Contains(p.RuntimeReason, tc.file) {
			t.Errorf("%s: %s (%s)", tc.name, p.RuntimeVersion, p.RuntimeReason)
		}
		if !strings.Contains(p.Command, "lib/python"+tc.want) {
			t.Errorf("%s: the bundle's stdlib isn't %s's: %s", tc.name, tc.want, p.Command)
		}
	}
	for spec, want := range map[string]string{
		// the default when it's allowed, else the newest allowed
		">=3.12":           "3.14",
		">=3.10,<3.14":     "3.13",
		"~=3.12":           "3.14",
		"~=3.12.1":         "3.12",
		"==3.13.*":         "3.13",
		">=3.12, !=3.14.*": "3.15",
		"<3.14,>3.12.99":   "3.13",
	} {
		files := pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n")
		files["pyproject.toml"] = strings.Replace(files["pyproject.toml"], `requires-python = ">=3.12"`, `requires-python = "`+spec+`"`, 1)
		p := detect(t, files, buildplan.Settings{})
		if line(p.RuntimeVersion) != want || !strings.Contains(p.RuntimeReason, "requires-python") {
			t.Errorf("%s: %s, want %s (%s)", spec, p.RuntimeVersion, want, p.RuntimeReason)
		}
	}
	// nothing says: the default
	files := pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n")
	files["pyproject.toml"] = strings.Replace(files["pyproject.toml"], "requires-python = \">=3.12\"\n", "", 1)
	if p := detect(t, files, buildplan.Settings{}); p.RuntimeVersion != buildplan.DefaultPython {
		t.Errorf("default: %s", p.RuntimeVersion)
	}
	// one not offered
	for file, body := range map[string]string{".python-version": "3.9\n", "pyproject.toml": "[project]\nrequires-python = \"<3.10\"\ndependencies = [\"flask\"]\n"} {
		files := pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n", file, body)
		if _, err := buildplan.Detect(repo(files), buildplan.Settings{}); err == nil || !strings.Contains(err.Error(), "3.14") {
			t.Errorf("%s %q: want an error listing the lines, got %v", file, body, err)
		}
	}
}

// A JavaScript app with a requirements.txt beside it (a script's) stays
// JavaScript; a Django app with a package.json (its CSS) is Python.
func TestPythonOrJavaScript(t *testing.T) {
	js := map[string]string{"package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^8"}}`, "bun.lock": bunLock,
		"requirements.txt": "requests\n"}
	if p := detect(t, js, buildplan.Settings{}); p.Toolchain == "python" {
		t.Errorf("js: %+v", p)
	}
	dj := pyproject([]string{"django", "gunicorn"}, "manage.py", managePy, "package.json", `{"devDependencies":{"tailwindcss":"^4"}}`, "bun.lock", bunLock)
	if p := detect(t, dj, buildplan.Settings{}); p.Toolchain != "python" {
		t.Errorf("django: %+v", p)
	}
}

// A Python app's front-end assets: a package.json with a build script is
// built with the pinned Bun (fetched by its checksum, as a Node app's build
// fetches it), before collectstatic picks up what it built; node_modules
// never ships. A package.json with no build script builds nothing.
func TestAPythonAppsFrontEndIsBuilt(t *testing.T) {
	pkg := `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^8","tailwindcss":"^4"}}`
	p := detect(t, pyproject([]string{"django", "gunicorn", "whitenoise"}, "manage.py", managePy, "package.json", pkg, "bun.lock", bunLock), buildplan.Settings{})
	if p.Toolchain != "python" || p.Framework != "Django" {
		t.Fatalf("plan: %+v", p)
	}
	for _, want := range []string{"registry.npmjs.org/@oven/bun-linux-$a/-/bun-linux-$a-" + buildplan.BunVersion + ".tgz", "sha256sum -c",
		"bun install --frozen-lockfile && bun run build"} {
		if !strings.Contains(p.Install, want) {
			t.Errorf("install has no %q: %s", want, p.Install)
		}
	}
	all := p.Install + " && " + p.Command
	if b, c := strings.Index(all, "bun run build"), strings.Index(all, "collectstatic"); b < 0 || c < 0 || b > c {
		t.Errorf("the assets are built after collectstatic: %s", all)
	}
	if !strings.Contains(p.Command, "--exclude=./node_modules") {
		t.Errorf("node_modules ships: %s", p.Command)
	}
	if slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "package.json") || strings.Contains(w, "front-end") }) {
		t.Errorf("warnings: %q", p.Warnings)
	}

	// Flask's too, with npm's lockfile: still Bun's install, as a PHP app's
	p = detect(t, map[string]string{"requirements.txt": "flask\n", "app.py": "app = Flask(__name__)\n",
		"package.json": pkg, "package-lock.json": "{}"}, buildplan.Settings{})
	if !strings.Contains(p.Install, "bun install && bun run build") {
		t.Errorf("flask: %s", p.Install)
	}

	// no build script: no Bun
	p = detect(t, pyproject([]string{"django"}, "manage.py", managePy, "package.json", `{"devDependencies":{"prettier":"^3"}}`), buildplan.Settings{})
	if strings.Contains(p.Install, "bun") {
		t.Errorf("no build script: %s", p.Install)
	}
}

// gunicorn runs 2 workers of 4 threads each (gthread): a sync worker on its
// own serves one request at a time. An app that sets its workers - a
// gunicorn.conf.py, or its own start command - keeps its own.
func TestGunicornsWorkers(t *testing.T) {
	files := pyproject([]string{"flask", "gunicorn"}, "app.py", "app = Flask(__name__)\n")
	if p := detect(t, files, buildplan.Settings{}); !strings.Contains(p.Run, "--workers 2 --threads 4") {
		t.Errorf("run: %s", p.Run)
	}
	files["gunicorn.conf.py"] = "workers = 8\n"
	if p := detect(t, files, buildplan.Settings{}); p.Run != "-m gunicorn --bind 0.0.0.0:$PORT app:app" {
		t.Errorf("gunicorn.conf.py: %s", p.Run)
	}
	delete(files, "gunicorn.conf.py")
	if p := detect(t, files, buildplan.Settings{Run: "-m gunicorn --bind 0.0.0.0:$PORT -w 3 app:app"}); p.Run != "-m gunicorn --bind 0.0.0.0:$PORT -w 3 app:app" {
		t.Errorf("start command: %s", p.Run)
	}
	// uvicorn: one process, as before
	if p := detect(t, pyproject([]string{"fastapi"}, "main.py", "app = FastAPI()\n"), buildplan.Settings{}); strings.Contains(p.Run, "--workers") {
		t.Errorf("uvicorn: %s", p.Run)
	}
}

// A uv.lock that doesn't match pyproject.toml fails the build, saying to
// run uv lock: the build installs what was locked, or nothing.
func TestAStaleUVLockFailsTheBuild(t *testing.T) {
	p := detect(t, pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n"), buildplan.Settings{})
	if !strings.Contains(p.Install, "uv export --locked") || strings.Contains(p.Install, "--frozen") ||
		!strings.Contains(p.Install, "uv.lock is out of date") || !strings.Contains(p.Install, "run uv lock") {
		t.Errorf("install: %s", p.Install)
	}
	// --locked resolves, so it needs the build's Python: uv finds none on
	// PATH, and a .python-version names a line it must find
	if !strings.Contains(p.Install, "-o .homeport-build/requirements.txt --python .homeport-build/python/bin/python3.14 ") {
		t.Errorf("uv export isn't told the build's Python: %s", p.Install)
	}
}

// An app's own top-level bin/ folder can't ship: bin is the interpreter.
func TestAPythonBundleRefusesABinFolder(t *testing.T) {
	p := detect(t, pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n"), buildplan.Settings{})
	if !strings.Contains(p.Command, "[ ! -d bin ]") {
		t.Errorf("command: %s", p.Command)
	}
}

func TestTheFilesPythonDetectionReads(t *testing.T) {
	for _, f := range []string{"pyproject.toml", "uv.lock", "requirements.txt", ".python-version", "runtime.txt", "manage.py", "main.py", "app.py",
		"wsgi.py", "app/main.py", "app/__init__.py", "alembic.ini", "Pipfile", "poetry.lock"} {
		if !slices.Contains(buildplan.Files(), f) {
			t.Errorf("%s not listed", f)
		}
	}
}

// Every offered Python is pinned for both architectures, and the default is
// one of them.
func TestPythonsArePinned(t *testing.T) {
	versions := buildplan.PythonVersions()
	if len(versions) == 0 || !slices.Contains(versions, buildplan.DefaultPython) {
		t.Fatalf("versions %v, default %s", versions, buildplan.DefaultPython)
	}
	p := detect(t, pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n"), buildplan.Settings{})
	for _, arch := range []string{"x86_64", "aarch64"} {
		if !strings.Contains(p.Install, arch+")") {
			t.Errorf("no %s: %s", arch, p.Install)
		}
	}
}

// The build settings' Python, one of the lines homeport runs, over what the
// project says; a deploy's over the app's.
func TestThePythonSet(t *testing.T) {
	files := pyproject([]string{"flask"}, "app.py", "app = Flask(__name__)\n", ".python-version", "3.13\n")
	p := detect(t, files, buildplan.Settings{Python: "3.12"})
	if !strings.HasPrefix(p.RuntimeVersion, "3.12.") || !strings.Contains(p.RuntimeReason, "the build settings") || !strings.Contains(p.Command, "lib/python3.12") {
		t.Errorf("set: %s (%s)", p.RuntimeVersion, p.RuntimeReason)
	}
	if s := (buildplan.Settings{Python: "3.12"}).With(buildplan.Settings{Python: "3.15"}); s.Python != "3.15" || s.IsZero() {
		t.Errorf("with: %+v", s)
	}
	for _, bad := range []string{"3.11", "3", "3.14.9", "python3.14", "2.7"} {
		if err := (buildplan.Settings{Python: bad}).Check(); err == nil || !strings.Contains(err.Error(), "3.12, 3.13, 3.14, 3.15") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := (buildplan.Settings{Python: "3.15"}).Check(); err != nil {
		t.Error(err)
	}
	// a JavaScript app with a Python set: it isn't Python's to say
	if _, err := buildplan.Detect(repo(map[string]string{"package.json": `{"scripts":{"build":"vite build"}}`, "bun.lock": bunLock}),
		buildplan.Settings{Python: "3.13"}); err == nil || !strings.Contains(err.Error(), "Python") {
		t.Errorf("js: %v", err)
	}
}
