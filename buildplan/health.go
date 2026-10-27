package buildplan

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxHealthPath is the longest health check path an app may have.
const MaxHealthPath = 200

// healthPathRe: a path on the app, in the characters homeportd's add takes
// (^/[A-Za-z0-9._/-]*$), with no empty segment - so never "//host".
var healthPathRe = regexp.MustCompile(`^/([A-Za-z0-9._-]+/?)*$`)

// CheckHealthPath says whether p may be the path an app's deploys are
// health-checked on: "" (the default, "/"), or a path on the app - starting
// with /, no host, query or spaces, no "..", at most MaxHealthPath
// characters. homeport.yaml's health: and the control plane's setting are
// held to it.
func CheckHealthPath(p string) error {
	switch {
	case p == "":
		return nil
	case len(p) > MaxHealthPath:
		return errors.New("at most 200 characters")
	case !strings.HasPrefix(p, "/"):
		return errors.New("a path on the app, starting with / - like /api/health")
	case strings.HasPrefix(p, "//"):
		return errors.New("a path on the app, not another address: like /api/health")
	case slices.Contains(strings.Split(p, "/"), ".."):
		return errors.New("a path on the app, without ..")
	case !healthPathRe.MatchString(p):
		return errors.New("a path of letters, digits and . _ - /, no query, no spaces - like /api/health")
	}
	return nil
}

// checkFileHealth is CheckHealthPath said about homeport.yaml's health:.
func checkFileHealth(p string) error {
	if err := CheckHealthPath(p); err != nil {
		return fmt.Errorf("%s: health %q: %w", ConfigFile, p, err)
	}
	return nil
}

// FileHealth is homeport.yaml's health path alone, checked; "" when it says
// none. The control plane reads it from a deploy's commit (or the tree the
// CLI uploaded): the file's wins over the app's setting.
func FileHealth(b []byte) (string, error) {
	var cfg struct {
		Health string `yaml:"health"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if err := checkFileHealth(cfg.Health); err != nil {
		return "", err
	}
	return cfg.Health, nil
}
