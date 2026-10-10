package buildplan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release runner (.homeport/release.php), run as homeportd runs a
// release - ./bin php-cli .homeport/release.php ./bin <the app's release> -
// here with the PHP on PATH, and a bin that logs what it's asked: the app's
// release command first, then artisan view:cache. The app's failure is the
// release's (the deploy stops), and caches no views; a view:cache that fails
// is said, and the deploy goes on (views compile as they're first shown).
func TestTheReleaseRunnerCachesViewsAfterTheAppsRelease(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php here")
	}
	run := func(t *testing.T, fail string, args ...string) (calls, stderr string, code int) {
		t.Helper()
		dir := t.TempDir()
		write(t, dir, releasePHPF, releasePHP, 0o644)
		write(t, dir, "bin", "#!/bin/sh\necho \"$*\" >> calls\ncase \"$*\" in *\"$FAIL\"*) [ -n \"$FAIL\" ] && { echo \"$* failed\" >&2; exit 3; } ;; esac\nexit 0\n", 0o755)
		cmd := exec.Command(php, append([]string{releasePHPF, "./bin"}, args...)...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "FAIL="+fail)
		var e strings.Builder
		cmd.Stderr = &e
		err := cmd.Run()
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return string(b), e.String(), code
	}
	if calls, _, code := run(t, ""); calls != "php-cli artisan view:cache\n" || code != 0 {
		t.Errorf("no release of the app's: %q %d", calls, code)
	}
	if calls, _, code := run(t, "", "php-cli", "artisan", "migrate", "--force"); calls != "php-cli artisan migrate --force\nphp-cli artisan view:cache\n" || code != 0 {
		t.Errorf("with the app's: %q %d", calls, code)
	}
	if calls, stderr, code := run(t, "migrate", "php-cli", "artisan", "migrate", "--force"); calls != "php-cli artisan migrate --force\n" || code != 3 || !strings.Contains(stderr, "failed") {
		t.Errorf("the app's failed: %q %q %d", calls, stderr, code)
	}
	if calls, stderr, code := run(t, "view:cache", "php-cli", "artisan", "migrate", "--force"); code != 0 || !strings.Contains(calls, "view:cache") || !strings.Contains(stderr, "homeport: ") {
		t.Errorf("view:cache failed: %q %q %d", calls, stderr, code)
	}
}
