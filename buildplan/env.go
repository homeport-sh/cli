package buildplan

import (
	"encoding/base64"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/homeport-sh/cli/envfile"
)

// An app's default environment (Plan.Env): what its framework reads that
// homeport would otherwise have the app set - Rocket listens on
// ROCKET_ADDRESS and ROCKET_PORT, not HOST and PORT. Detection says the
// values; the bundle carries them, a NAME=value line each, in
// BundleEnvFile; homeportd sets each one that neither it (PORT, NODE_ENV)
// nor the app's own variables do, so a person's value always wins. A
// value may name $PORT and $HOST, as a run command does: homeportd puts
// in the instance's port and address. Nothing else of a shell's.

// BundleEnvFile is where a bundle carries its default environment.
const BundleEnvFile = ".homeport/env"

// envValueVars: what a default's value may name, $PORT and $HOST alone.
var envValueVars = regexp.MustCompile(`\$(\{(PORT|HOST)\}|(PORT|HOST)\b)`)

// CheckEnv says whether env is a default environment homeportd takes: names
// an app may set (envfile), values on one line, naming no variable but
// $PORT and $HOST.
func CheckEnv(env map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := env[k]
		switch {
		case !envfile.ValidName(k) || envfile.Reserved(k):
			return fmt.Errorf("default environment: %q isn't a name an app may set", k)
		case strings.ContainsAny(v, "\n\r\x00"):
			return fmt.Errorf("default environment: %s's value is more than one line", k)
		case strings.Contains(envValueVars.ReplaceAllString(v, ""), "$"):
			return fmt.Errorf("default environment: %s's value may name only $PORT and $HOST", k)
		}
	}
	return nil
}

// envLines is env as BundleEnvFile holds it, sorted.
func envLines(env map[string]string) string {
	var s strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		s.WriteString(k + "=" + env[k] + "\n")
	}
	return s.String()
}

// bundleEnv writes env into the bundle b, after what made it.
func bundleEnv(b string, env map[string]string) string {
	return "mkdir -p " + b + "/.homeport && printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(envLines(env))) + "' | base64 -d > " + b + "/" + BundleEnvFile
}

// shipEnv puts p's default environment in its bundle. A build that
// doesn't make homeport's bundle has nowhere to carry it: a person sets
// those variables, and the plan says which.
func shipEnv(p *Plan) error {
	if len(p.Env) == 0 {
		p.Env = nil
		return nil
	}
	if err := CheckEnv(p.Env); err != nil {
		return err
	}
	if p.Kind != Bundle || p.assemble == "" {
		said := strings.Split(strings.TrimSuffix(envLines(p.Env), "\n"), "\n")
		p.Warnings = append(p.Warnings, "the build doesn't make homeport's bundle, which would set "+p.Framework+"'s defaults: set them as the app's environment variables ("+
			strings.Join(said, ", ")+")")
		p.Env = nil
		return nil
	}
	p.Command = join(p.Command, bundleEnv(p.Artifact, p.Env))
	return nil
}
