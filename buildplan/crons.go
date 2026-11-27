package buildplan

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// MaxCrons is how many cron jobs an app may have (the control plane's
// limit, design/cron.md in homeport-sh/cloud).
const MaxCrons = 25

// Cron is a job homeport.yaml declares: a command, args to the app's bin
// run in a sandbox of its own, on a schedule (5 fields or a macro) in a
// zone, stopped at its timeout (seconds). The control plane keeps them as
// the app's declared jobs on each deploy, checking the schedule against
// the team's plan; a person's own jobs are left alone.
type Cron struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Schedule string `json:"schedule"`
	Timezone string `json:"timezone"`
	Timeout  int    `json:"timeout"`
}

type cronConfig struct {
	Command  string `yaml:"command"`
	Schedule string `yaml:"schedule"`
	Timezone string `yaml:"timezone"`
	Timeout  string `yaml:"timeout"`
}

var (
	cronNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// a schedule's shape: 5 fields or a macro, checked in full by the
	// control plane (its internal/cron), the one parser of them
	cronScheduleRe = regexp.MustCompile(`^(@[a-z]{1,10}|[0-9A-Za-z*/,?-]{1,40}( [0-9A-Za-z*/,?-]{1,40}){4})$`)
)

const defaultCronTimeout = 600

// crons checks homeport.yaml's crons and says them by name; nil for none.
func crons(cfg map[string]cronConfig) ([]Cron, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	if len(cfg) > MaxCrons {
		return nil, fmt.Errorf("at most %d crons", MaxCrons)
	}
	out := make([]Cron, 0, len(cfg))
	for name, c := range cfg {
		j, err := cron(name, c)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	slices.SortFunc(out, func(a, b Cron) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func cron(name string, c cronConfig) (Cron, error) {
	j := Cron{Name: name, Command: strings.TrimSpace(c.Command), Schedule: strings.TrimSpace(c.Schedule), Timezone: c.Timezone, Timeout: defaultCronTimeout}
	if j.Timezone == "" {
		j.Timezone = "UTC"
	}
	if c.Timeout != "" {
		d, err := time.ParseDuration(strings.TrimSpace(c.Timeout))
		if err != nil {
			return Cron{}, fmt.Errorf("cron %q: timeout %q: a duration, like 300s or 5m", name, c.Timeout)
		}
		if d%time.Second != 0 {
			return Cron{}, fmt.Errorf("cron %q: timeout 10s to 1h, in whole seconds (%q)", name, c.Timeout)
		}
		j.Timeout = int(d / time.Second)
	}
	return j, checkCron(j)
}

// checkCron says whether j is a job the control plane can keep.
func checkCron(j Cron) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("cron %q: "+format, append([]any{j.Name}, a...)...)
	}
	switch {
	case !cronNameRe.MatchString(j.Name):
		return bad("a name of lowercase letters, digits and -, starting with a letter, at most 40")
	case strings.TrimSpace(j.Command) == "":
		return bad("a command (args to the app's binary)")
	}
	if err := checkArgs(fmt.Sprintf("cron %q command", j.Name), j.Command); err != nil {
		return err
	}
	switch {
	case j.Schedule == "":
		return bad("a schedule (5 fields, like \"45 3 * * *\", or @daily)")
	case !cronScheduleRe.MatchString(j.Schedule):
		return bad("schedule %q: 5 fields, like \"45 3 * * *\", or a macro like @daily", j.Schedule)
	}
	if _, err := time.LoadLocation(j.Timezone); err != nil || j.Timezone == "" || j.Timezone == "Local" {
		return bad("timezone %q: an IANA zone, like America/New_York", j.Timezone)
	}
	if j.Timeout < 10 || j.Timeout > 3600 {
		return bad("timeout 10s to 1h, in whole seconds (%ds)", j.Timeout)
	}
	return nil
}

// CheckCrons says whether crons are jobs the control plane can keep: at
// most MaxCrons, each named once, each as homeport.yaml's are checked.
func CheckCrons(crons []Cron) error {
	if len(crons) > MaxCrons {
		return fmt.Errorf("at most %d crons", MaxCrons)
	}
	seen := map[string]bool{}
	for _, j := range crons {
		if seen[j.Name] {
			return fmt.Errorf("cron %q: named twice", j.Name)
		}
		seen[j.Name] = true
		if err := checkCron(j); err != nil {
			return err
		}
	}
	return nil
}

// FileCrons is homeport.yaml's crons alone, checked; nil when it has none.
// A deploy no builder planned (a CI upload) keeps them, read by the
// control plane from the commit.
func FileCrons(b []byte) ([]Cron, error) {
	var cfg struct {
		Crons map[string]cronConfig `yaml:"crons"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	c, err := crons(cfg.Crons)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return c, nil
}

var errStaticCrons = errors.New("crons: a static site has no server for them to run in")
