// Package cloud calls homeport.sh's API for the CLI (/v1/cli/): signing in
// from a terminal, and what a signed-in CLI does. Every call but signing in
// carries the CLI's token; the token never appears in an error.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/homeport-sh/cli/buildplan"
	"github.com/homeport-sh/cli/envfile"
)

// DefaultAPI is homeport.sh's API.
const DefaultAPI = "https://api.homeport.sh"

// ErrSignedOut: no token, or one that ended (revoked, expired).
var ErrSignedOut = errors.New("not signed in, or the sign-in ended: run `homeport login`")

// Error is a refusal: the API's own words and the field it's about. A
// sign-in's poll answers with RFC 8628's codes (Pending, SlowDown, ...).
type Error struct {
	Status  int
	Message string
	Field   string
}

func (e *Error) Error() string { return e.Message }

// Client calls the API at Base as Token ("" none).
type Client struct {
	Base      string
	Token     string
	UserAgent string
	// Timeout is a call's longest (0: 30s); queueing a deploy, which waits
	// for its upload to be checked, gets DeployTimeout
	Timeout time.Duration
}

// DeployTimeout is how long queueing a deploy may take: its upload is
// copied and read through before it answers.
const DeployTimeout = 10 * time.Minute

func (c *Client) http() *http.Client {
	if c.Timeout > 0 {
		return &http.Client{Timeout: c.Timeout}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call sends in (nil: none) as JSON and decodes a 2xx answer into out (nil:
// ignore it).
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	return c.callWith(ctx, c.http(), method, path, in, out)
}

func (c *Client) callWith(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("couldn't reach %s: %v", c.Base, ue.Err)
		}
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if out == nil || len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	var p struct {
		Error string `json:"error"`
		Field string `json:"field"`
	}
	_ = json.Unmarshal(raw, &p)
	if res.StatusCode == http.StatusUnauthorized && path != "/v1/cli/device/token" {
		return ErrSignedOut
	}
	e := &Error{Status: res.StatusCode, Message: p.Error, Field: p.Field}
	if e.Message == "" {
		e.Message = fmt.Sprintf("homeport answered %d %s; try again", res.StatusCode, http.StatusText(res.StatusCode))
	}
	if res.StatusCode == http.StatusTooManyRequests && res.Header.Get("Retry-After") != "" {
		e.Message += " (retry after " + res.Header.Get("Retry-After") + "s)"
	}
	return e
}

// DeviceCode is a sign-in from this terminal, waiting to be approved.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDevice asks to sign in as a device called name.
func (c *Client) StartDevice(ctx context.Context, name string) (*DeviceCode, error) {
	var d DeviceCode
	err := c.call(ctx, "POST", "/v1/cli/device", map[string]string{"name": name}, &d)
	return &d, err
}

// User is who a token is.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Login string `json:"login"`
}

// Token is what an approved sign-in gives, once.
type Token struct {
	Token     string    `json:"token"`
	TokenID   string    `json:"token_id"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

// The poll's answers before a token (RFC 8628).
const (
	Pending  = "authorization_pending"
	SlowDown = "slow_down"
	Expired  = "expired_token"
	Denied   = "access_denied"
)

// PollDevice asks whether the sign-in was approved: a *Error whose Message
// is Pending, SlowDown, Expired or Denied until it is (or isn't), then the
// token.
func (c *Client) PollDevice(ctx context.Context, deviceCode string) (*Token, error) {
	var t Token
	err := c.call(ctx, "POST", "/v1/cli/device/token", map[string]string{"device_code": deviceCode}, &t)
	return &t, err
}

// Team is one of the user's teams.
type Team struct {
	ID   string
	Name string
	Slug string
	Role string
}

// WhoAmI is who the token is, on which device, in which teams, and where
// the dashboard is.
type WhoAmI struct {
	User  User `json:"user"`
	Token struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"token"`
	Teams     []Team `json:"teams"`
	Dashboard string `json:"dashboard"`
}

// WhoAmI is who the token is.
func (c *Client) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var w WhoAmI
	err := c.call(ctx, "GET", "/v1/cli/whoami", nil, &w)
	return &w, err
}

// Logout revokes the token.
func (c *Client) Logout(ctx context.Context) error {
	return c.call(ctx, "DELETE", "/v1/cli/token", nil, nil)
}

// Deploy is one deploy of an environment.
type Deploy struct {
	ID        string
	SHA       string
	Ref       string
	Status    string // pending, uploaded, deploying, live, failed, superseded
	Detail    string
	CreatedAt time.Time
	Build     string
	// RollbackOf: a rollback's, the deploy whose build it shipped again;
	// By: who rolled back
	RollbackOf string
	By         string
	// Rollback: an earlier release that can be rolled back to (nil: not)
	Rollback *RollbackOffer
}

// RollbackOffer is what rolling back to an earlier release would ship:
// its build, kept until KeptUntil. Release: its release command, run only
// if asked; BuildVariablesDiffer: it was built with other build variables
// than the environment has now.
type RollbackOffer struct {
	KeptUntil            time.Time
	Release              string
	BuildVariablesDiffer bool
}

// App is one of a team's environments of an app.
type App struct {
	ID          string
	Name        string
	ProjectName string
	Environment string
	Branch      string
	Repo        string
	// Provider is where its code comes from: github, gitlab, …, upload,
	// or cli (no repository: deployed from the CLI)
	Provider   string
	Domain     string
	Builds     string // hosted, ci or upload
	Status     string
	LastDeploy *Deploy
	// Deploys: its latest, newest first (App alone answers them)
	Deploys []Deploy
}

// Apps are a team's environments.
func (c *Client) Apps(ctx context.Context, team string) ([]App, error) {
	var out []App
	err := c.call(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/apps", nil, &out)
	return out, err
}

// NewApp is an app made from the CLI: no repository, its code what
// `homeport deploy` uploads.
type NewApp struct {
	Name string `json:"name"`
	// Environment is the one it starts with ("": production)
	Environment string `json:"environment,omitempty"`
	// Root is the app's folder in what deploy uploads ("": its top)
	Root string `json:"root,omitempty"`
}

// CreateApp makes a team an app deployed from the CLI, and answers its
// environment, as Apps lists it.
func (c *Client) CreateApp(ctx context.Context, team string, in NewApp) (*App, error) {
	var out App
	if err := c.call(ctx, "POST", "/v1/cli/teams/"+url.PathEscape(team)+"/apps", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// App is one of them.
func (c *Client) App(ctx context.Context, team, app string) (*App, error) {
	var out App
	err := c.call(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/apps/"+url.PathEscape(app), nil, &out)
	return &out, err
}

// SourceSlot is where a working tree is PUT.
type SourceSlot struct {
	ID       string
	URL      string
	MaxBytes int64
}

// StartSource asks for a place to upload a working tree of size bytes.
func (c *Client) StartSource(ctx context.Context, team, app string, size int64) (*SourceSlot, error) {
	var out SourceSlot
	err := c.call(ctx, "POST", "/v1/cli/teams/"+url.PathEscape(team)+"/apps/"+url.PathEscape(app)+"/sources",
		map[string]int64{"bytes": size}, &out)
	return &out, err
}

// Upload PUTs size bytes of body to a slot's link. The link is signed for
// exactly that: the token isn't sent with it.
func (c *Client) Upload(ctx context.Context, link string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", link, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/gzip")
	res, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("uploading: %v", errors.Unwrap(err))
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("the upload was refused (%d); try again", res.StatusCode)
	}
	return nil
}

// DeployRequest deploys an uploaded working tree, built as SHA, with a
// change for this deploy alone - or saved (Save), with Unset fields emptied.
type DeployRequest struct {
	Source   string             `json:"source"`
	SHA      string             `json:"sha"`
	Settings buildplan.Settings `json:"settings"`
	Unset    []string           `json:"unset,omitempty"`
	Save     bool               `json:"save,omitempty"`
}

// Deploy queues a build of an uploaded working tree; its id.
func (c *Client) Deploy(ctx context.Context, team, app string, in DeployRequest) (string, error) {
	var out struct{ Build string }
	err := c.callWith(ctx, &http.Client{Timeout: DeployTimeout}, "POST", "/v1/cli/teams/"+url.PathEscape(team)+"/apps/"+url.PathEscape(app)+"/deploys", in, &out)
	return out.Build, err
}

// Build is a hosted build, as it goes.
type Build struct {
	ID      string
	Status  string // queued, running, succeeded, failed, superseded, canceled
	Reason  string
	Deploy  string
	Log     string // its end, as it's written
	SHA     string
	Trigger string
}

// Build is one of the team's builds.
func (c *Client) Build(ctx context.Context, team, build string) (*Build, error) {
	var out Build
	err := c.call(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/builds/"+url.PathEscape(build), nil, &out)
	return &out, err
}

// DeployStatus is one of the team's deploys.
func (c *Client) DeployStatus(ctx context.Context, team, deploy string) (*Deploy, error) {
	var out Deploy
	err := c.call(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/deploys/"+url.PathEscape(deploy), nil, &out)
	return &out, err
}

// NewToken is a token made for CI: shown once.
type NewToken struct {
	Token   string
	Session struct {
		ID   string
		Name string
	}
}

// CreateToken makes a named token for CI, as the signed-in person, for one
// environment (app, by id) of team (by id): it acts there alone.
func (c *Client) CreateToken(ctx context.Context, name, team, app string) (*NewToken, error) {
	var out NewToken
	err := c.call(ctx, "POST", "/v1/cli/tokens", map[string]string{"name": name, "team": team, "app": app}, &out)
	return &out, err
}
func appPath(team, app string) string {
	return "/v1/cli/teams/" + url.PathEscape(team) + "/apps/" + url.PathEscape(app)
}

// Env is an environment's variables, by name - no value comes back - and
// of its file variables what may be shown (envfile.FileMeta).
type Env struct {
	Names []string // its own
	Files []FileVar
	// Vars: every variable it gets, its own and inherited, with who sees
	// each and where it comes from (none from a platform before them)
	Vars []EnvVar
	// NeedsRebuild: what its build sees changed since its live release was
	// built
	NeedsRebuild bool
}

// EnvVar is one variable an environment gets, by name.
type EnvVar struct {
	Name      string
	Available envfile.Availability
	Source    string // own, global, addon or homeport
	Why       string // homeport's: what its framework needs it for
	Fixed     bool   // who sees it can't be changed: its framework reads it one way
	PerDeploy bool   // homeport's, made for each deploy and never stored
	// Roles: the parts of the app with a value of their own (web, release,
	// a process's name), sorted; RolesOnly: none for every part
	// (design/variable-roles.md in homeport-sh/cloud).
	Roles     []string
	RolesOnly bool
	// Secret: its value is never shown again; else a variable, its Value
	// shown (design/secret-variables.md in homeport-sh/cloud)
	Secret bool
	Value  string
	// Refs: the variables its value names (${{NAME}}), a secret's too;
	// Resolved: a variable's value naming others as the app gets it
	// (design/variable-references.md in homeport-sh/cloud)
	Refs     []string
	Resolved string
}

// FileVar is one file variable: its name and what may be shown of it.
type FileVar struct {
	Name string
	Meta envfile.FileMeta
}

// Env is an environment's variables (Env).
func (c *Client) Env(ctx context.Context, team, app string) (*Env, error) {
	var out Env
	err := c.call(ctx, "GET", appPath(team, app)+"/env", nil, &out)
	return &out, err
}

// SetEnv sets and removes an environment's variables - files the file
// variables, their bytes as they are - and answers its names after.
func (c *Client) SetEnv(ctx context.Context, team, app string, set map[string]string, files map[string][]byte, unset []string) ([]string, error) {
	return c.ChangeEnv(ctx, team, app, EnvChange{Set: set, Files: files, Unset: unset})
}

// EnvChange is one change of an environment's variables.
type EnvChange struct {
	Set   map[string]string
	Files map[string][]byte // file variables, their bytes as they are
	Unset []string
	// Available: who sees each, by name - set with it, or alone, its value
	// kept. A name set without one keeps its own, or a new one takes its
	// name's (envfile.DefaultAvailable).
	Available map[string]envfile.Availability
	// Roles: values for one part of the app alone, by role (web, release,
	// a process's name) - set, or that role's own removed. Never a build's.
	Roles map[string]RoleChange
	// Secret: which each is, by name - a secret (never shown again) or a
	// variable (shown). A name without one keeps its kind; a new one is a
	// secret.
	Secret map[string]bool
}

// RoleChange is one role's values set and removed, and which each is.
type RoleChange struct {
	Set    map[string]string `json:"set,omitempty"`
	Unset  []string          `json:"unset,omitempty"`
	Secret map[string]bool   `json:"secret,omitempty"`
}

// ChangeEnv makes one change of an environment's variables (EnvChange),
// and answers its names after.
func (c *Client) ChangeEnv(ctx context.Context, team, app string, ch EnvChange) ([]string, error) {
	var out []string
	body := map[string]any{"set": ch.Set, "unset": ch.Unset}
	if len(ch.Files) > 0 {
		body["files"] = ch.Files // base64, as encoding/json writes []byte
	}
	if len(ch.Available) > 0 {
		body["available"] = ch.Available
	}
	if len(ch.Secret) > 0 {
		body["secret"] = ch.Secret
	}
	if len(ch.Roles) > 0 {
		// a platform without them refuses the field: nothing is saved,
		// never as a value for every role
		body["roles"] = ch.Roles
	}
	err := c.call(ctx, "POST", appPath(team, app)+"/env", body, &out)
	return out, err
}

// Rebuild builds an environment's live commit again with its variables as
// they are now, and answers the build.
func (c *Client) Rebuild(ctx context.Context, team, app string) (string, error) {
	var out struct {
		Build string `json:"build"`
	}
	err := c.call(ctx, "POST", appPath(team, app)+"/rebuild", nil, &out)
	return out.Build, err
}

// Rollback ships an earlier release's build again (deploy, one its app's
// detail offers), its release command too if runRelease; the deploy that
// ships it.
func (c *Client) Rollback(ctx context.Context, team, app, deploy string, runRelease bool) (string, error) {
	var out struct {
		Deploy string `json:"deploy"`
	}
	err := c.call(ctx, "POST", appPath(team, app)+"/rollback", map[string]any{"deploy": deploy, "run_release": runRelease}, &out)
	return out.Deploy, err
}

// LogLine is one line of an environment's runtime log.
type LogLine struct {
	Time    time.Time
	Level   string
	Message string
	Process string
}

// Logs is a page of runtime logs, and the cursor to read on from.
type Logs struct {
	Cursor string
	Lines  []LogLine
}

// Logs are an environment's newest runtime log lines, or those since cursor.
func (c *Client) Logs(ctx context.Context, team, app, cursor string) (*Logs, error) {
	p := appPath(team, app) + "/logs"
	if cursor != "" {
		p += "?cursor=" + url.QueryEscape(cursor)
	}
	var out Logs
	err := c.call(ctx, "GET", p, nil, &out)
	return &out, err
}

// raw calls and answers the JSON as it came: views passed on whole.
func (c *Client) raw(ctx context.Context, method, path string, in any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.call(ctx, method, path, in, &out)
	return out, err
}

// Database is an environment's database: never its password.
func (c *Client) Database(ctx context.Context, team, app string) (json.RawMessage, error) {
	return c.raw(ctx, "GET", appPath(team, app)+"/database", nil)
}

// CreateDatabase makes an environment its database.
func (c *Client) CreateDatabase(ctx context.Context, team, app string) (json.RawMessage, error) {
	return c.raw(ctx, "POST", appPath(team, app)+"/database", map[string]any{})
}

// AttachDatabase attaches one of the team's databases to an environment.
func (c *Client) AttachDatabase(ctx context.Context, team, app, database string) (json.RawMessage, error) {
	return c.raw(ctx, "POST", appPath(team, app)+"/database/attach", map[string]string{"database": database})
}

// DetachDatabase detaches an environment's database (it isn't deleted).
func (c *Client) DetachDatabase(ctx context.Context, team, app string) (json.RawMessage, error) {
	return c.raw(ctx, "POST", appPath(team, app)+"/database/detach", map[string]any{})
}

// Usage is a team's month so far, and what it's likely to cost.
func (c *Client) Usage(ctx context.Context, team string) (json.RawMessage, error) {
	return c.raw(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/usage", nil)
}

// Domain is one of an app's platform domains: a tenant's domain on the
// Domains API (design/platforms.md in homeport cloud), which an app's own
// code adds as its tenants sign up.
type Domain struct {
	Domain string `json:"domain"`
	// Status: pending, verifying, live or failed
	Status string `json:"status"`
	// Why it isn't live ("" when it is)
	Why string `json:"why,omitempty"`
	// Wildcard: the app's wildcard it's under, which needs no records
	Wildcard string `json:"wildcard,omitempty"`
	// Records: what to set at the domain's DNS provider
	Records   []DNSRecord `json:"records"`
	CreatedAt string      `json:"created_at"`
	CheckedAt string      `json:"checked_at,omitempty"`
	// DropsAt: a pending one is dropped then, unless it's proved
	DropsAt string `json:"drops_at,omitempty"`
	// Certificate: a wildcard's tenants are served its one certificate
	// ("wildcard") or each their own ("per-name"; CertificateWhy says why);
	// "" for anything else
	Certificate    string `json:"certificate,omitempty"`
	CertificateWhy string `json:"certificate_why,omitempty"`
}

// DNSRecord is a record to set: Name relative to the root domain, as DNS
// providers' forms ask, FullName the whole name.
type DNSRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Value    string `json:"value"`
}

// Domains is a page of an app's platform domains, and the after for the
// next ("" for none). Live is what the bill counts, Total every one,
// Included how many the plan includes.
type Domains struct {
	Domains  []Domain `json:"domains"`
	Next     string   `json:"next,omitempty"`
	Live     int      `json:"live"`
	Total    int      `json:"total"`
	Included int64    `json:"included"`
}

// Domains is a page of an app's platform domains: the first, or those
// after a page's Next.
func (c *Client) Domains(ctx context.Context, team, app, after string) (*Domains, error) {
	p := appPath(team, app) + "/domains"
	if after != "" {
		p += "?after=" + url.QueryEscape(after)
	}
	var out Domains
	err := c.call(ctx, "GET", p, nil, &out)
	return &out, err
}

// AddDomain adds an app a platform domain; the same one again is the one
// it has.
func (c *Client) AddDomain(ctx context.Context, team, app, domain string) (*Domain, error) {
	var out Domain
	err := c.call(ctx, "POST", appPath(team, app)+"/domains", map[string]string{"domain": domain}, &out)
	return &out, err
}

// Domain is one of an app's platform domains.
func (c *Client) Domain(ctx context.Context, team, app, domain string) (*Domain, error) {
	var out Domain
	err := c.call(ctx, "GET", appPath(team, app)+"/domains/"+url.PathEscape(domain), nil, &out)
	return &out, err
}

// RemoveDomain removes one of an app's platform domains.
func (c *Client) RemoveDomain(ctx context.Context, team, app, domain string) error {
	return c.call(ctx, "DELETE", appPath(team, app)+"/domains/"+url.PathEscape(domain), nil, nil)
}

// PullEnv is an environment's add-on credentials (its database's and
// storage's variables), by name: never production's, never its own
// variables.
// Withheld names what production uses too, so isn't pulled.
func (c *Client) PullEnv(ctx context.Context, team, app string) (map[string]string, []string, error) {
	var out struct {
		Values   map[string]string
		Withheld []string
	}
	err := c.call(ctx, "GET", appPath(team, app)+"/env/addons", nil, &out)
	return out.Values, out.Withheld, err
}
