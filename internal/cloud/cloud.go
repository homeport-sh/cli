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
}

// App is one of a team's environments of an app.
type App struct {
	ID          string
	Name        string
	ProjectName string
	Environment string
	Branch      string
	Repo        string
	Domain      string
	Builds      string // hosted, ci or upload
	Status      string
	LastDeploy  *Deploy
}

// Apps are a team's environments.
func (c *Client) Apps(ctx context.Context, team string) ([]App, error) {
	var out []App
	err := c.call(ctx, "GET", "/v1/cli/teams/"+url.PathEscape(team)+"/apps", nil, &out)
	return out, err
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

// CreateToken makes a named token for CI, as the signed-in person.
func (c *Client) CreateToken(ctx context.Context, name string) (*NewToken, error) {
	var out NewToken
	err := c.call(ctx, "POST", "/v1/cli/tokens", map[string]string{"name": name}, &out)
	return &out, err
}
func appPath(team, app string) string {
	return "/v1/cli/teams/" + url.PathEscape(team) + "/apps/" + url.PathEscape(app)
}

// EnvNames are an environment's variables, by name: no value comes back.
func (c *Client) EnvNames(ctx context.Context, team, app string) ([]string, error) {
	var out struct{ Names []string }
	err := c.call(ctx, "GET", appPath(team, app)+"/env", nil, &out)
	return out.Names, err
}

// SetEnv sets and removes an environment's variables; its names after.
func (c *Client) SetEnv(ctx context.Context, team, app string, set map[string]string, unset []string) ([]string, error) {
	var out []string
	err := c.call(ctx, "POST", appPath(team, app)+"/env", map[string]any{"set": set, "unset": unset}, &out)
	return out, err
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

// PullEnv is an environment's add-on credentials (its database's and
// storage's variables), by name: never production's, never its own
// variables.
func (c *Client) PullEnv(ctx context.Context, team, app string) (map[string]string, error) {
	var out struct{ Values map[string]string }
	err := c.call(ctx, "GET", appPath(team, app)+"/env/addons", nil, &out)
	return out.Values, err
}
