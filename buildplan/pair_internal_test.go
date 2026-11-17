package buildplan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What a pair's build reads of rsc-kit's (embed/pair.mjs, run by the image's
// Bun): its marker says it made a server, and the backend's paths it names
// (rsc-kit 0.30.53+) are written for the host, a path a line - none, from an
// rsc-kit too old to name them or an app that names none, said. Paths
// homeport.yaml or the settings named (its args) are written in their place.
// One covering the host-call endpoint, which only the renderer calls, is
// refused: the host would open it to browsers.
func TestAPairsBuildReadsRSCKitsMarker(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun here")
	}
	for name, c := range map[string]struct {
		marker string // "" none
		args   []string
		env    []string
		ok     bool
		file   string // what it writes
		says   string // on stderr
	}{
		"paths":            {marker: `{"output":"server","backendPaths":["/auth","/api"]}`, ok: true, file: "/auth\n/api\n"},
		"none":             {marker: `{"output":"server","backendPaths":[]}`, ok: true, says: "homeport: no backend paths: only the renderer's calls reach Laravel"},
		"too old to say":   {marker: `{"version":"0.30.52","output":"server"}`, ok: true, says: "homeport: rsc-kit 0.30.52 names no backend paths"},
		"given":            {marker: `{"output":"server","backendPaths":["/auth"]}`, args: []string{"/api", "/hooks"}, ok: true, file: "/api\n/hooks\n"},
		"an export":        {marker: `{"output":"export","dir":"dist"}`, says: "homeport: rsc-kit made a static export"},
		"an export, given": {marker: `{"output":"export","dir":"dist"}`, args: []string{"/api"}, says: "static export"},
		"no marker":        {says: "homeport: the build made no .output/rsc-kit.json"},
		"not JSON":         {marker: `{`, says: "isn't JSON"},
		"not a list":       {marker: `{"output":"server","backendPaths":"/api"}`, says: "backendPaths"},
		"host-call":        {marker: `{"output":"server","backendPaths":["/__rsc"]}`, says: "the host-call endpoint"},
		"host-call, the app's": {marker: `{"output":"server","backendPaths":["/rpc/x"]}`, env: []string{"RSC_HOST_CALL_PATH=/rpc/x/call"},
			says: "the host-call endpoint"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.marker != "" {
				write(t, dir, RSCKitMarker, c.marker, 0o644)
			}
			write(t, dir, "pair.mjs", pairMJS, 0o644)
			cmd := exec.Command(bun, append([]string{"pair.mjs", "paths", "Laravel"}, c.args...)...)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), c.env...)
			out, err := cmd.CombinedOutput()
			if (err == nil) != c.ok {
				t.Fatalf("err %v:\n%s", err, out)
			}
			if c.says != "" && !strings.Contains(string(out), c.says) {
				t.Errorf("says %q, want %q", out, c.says)
			}
			if c.says == "" && len(out) > 0 {
				t.Errorf("says %q", out)
			}
			got, err := os.ReadFile(filepath.Join(dir, "paths"))
			if c.ok && (err != nil || string(got) != c.file) {
				t.Errorf("wrote %q (%v), want %q", got, err, c.file)
			}
			if !c.ok && err == nil {
				t.Errorf("wrote %q", got)
			}
		})
	}
}

func write(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
