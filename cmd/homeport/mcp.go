package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
	"github.com/homeport-sh/cli/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// `homeport mcp` is an MCP server on stdio, for an editor or an agent
// (Claude Code, Cursor) on this computer. It signs in as `homeport login`
// did - the same credentials, the same token API - so every call is the
// person's, held to what they may do. It never returns a variable's value
// or a secret, and it has no tool that deletes.

// mcpWritesPerMinute is how many changes the server makes a minute: an
// agent in a loop stops here, before the platform's own limits.
const mcpWritesPerMinute = 20

func (a *app) mcp(ctx context.Context, args []string) error {
	if err := parse(a.flags("mcp"), args); err != nil {
		return err
	}
	return a.mcpServer().Run(ctx, &mcp.StdioTransport{})
}

// writes counts the server's changes, a minute at a time.
type writes struct {
	mu    sync.Mutex
	start time.Time
	n     int
}

func (w *writes) allow() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.start) >= time.Minute {
		w.start, w.n = time.Now(), 0
	}
	w.n++
	return w.n <= mcpWritesPerMinute
}

// where is how a tool names an environment: by team, app and environment,
// or by a folder `homeport link` linked.
type where struct {
	Team        string `json:"team,omitempty" jsonschema:"the team's address (slug); with app"`
	App         string `json:"app,omitempty" jsonschema:"the app's name; without it, directory's link says which"`
	Environment string `json:"environment,omitempty" jsonschema:"the environment (default production)"`
	Directory   string `json:"directory,omitempty" jsonschema:"a folder linked with homeport link (default: the server's working folder)"`
}

func boolPtr(b bool) *bool { return &b }

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}

func changes(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)}
}

// result is a tool's answer: v as JSON.
func result(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

// refused is a tool's refusal, said to the agent (never a token).
func refused(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
}

var errUnconfirmed = errors.New("this changes the environment: ask the person first, then call again with confirm: true")

func (a *app) mcpServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "homeport", Title: "homeport.sh", Version: version}, &mcp.ServerOptions{
		Instructions: "Deploy and run apps on homeport.sh as the signed-in person (homeport login). " +
			"Tools that change things say so, and need confirm: true - ask the person before setting it. " +
			"Variable values and secrets are never returned.",
	})
	w := &writes{}
	// sub is the CLI writing into out, at dir, for a tool
	sub := func(out io.Writer, dir string) *app {
		if dir == "" {
			dir = a.wd
		}
		return &app{in: strings.NewReader(""), out: out, err: out, wd: dir, sleep: a.sleep, hostname: a.hostname,
			open: func(string) error { return errors.New("no browser") }}
	}
	// at is the environment where names, and the client to reach it
	at := func(ctx context.Context, in where) (*cloud.Client, *target, error) {
		c, err := a.client()
		if err != nil {
			return nil, nil, err
		}
		x := sub(io.Discard, in.Directory)
		if in.App != "" || in.Team != "" {
			t, err := x.resolve(ctx, c, in.Team, in.App, in.Environment)
			return c, t, err
		}
		l, _, err := config.FindLink(x.wd)
		if err != nil {
			return nil, nil, fmt.Errorf("%w (or name team and app)", err)
		}
		return c, &target{link: *l}, nil
	}
	write := func(confirm bool) error {
		if !confirm {
			return errUnconfirmed
		}
		if !w.allow() {
			return fmt.Errorf("too many changes in a minute (%d): wait, and say why to the person", mcpWritesPerMinute)
		}
		return nil
	}

	mcp.AddTool(s, &mcp.Tool{Name: "list_apps", Annotations: readOnly,
		Description: "Lists the signed-in person's teams and each team's apps and environments: name, environment, how it's built (hosted: homeport builds it; ci: its own CI deploys it), its address and its status. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			c, err := a.client()
			if err != nil {
				return refused(err)
			}
			who, err := c.WhoAmI(ctx)
			if err != nil {
				return refused(err)
			}
			type env struct{ App, Environment, Builds, Address, Status string }
			type team struct {
				Team, Role string
				Apps       []env
			}
			var out []team
			for _, t := range who.Teams {
				apps, err := c.Apps(ctx, t.ID)
				if err != nil {
					return refused(err)
				}
				tm := team{Team: t.Slug, Role: t.Role, Apps: []env{}}
				for _, e := range apps {
					addr := ""
					if e.Domain != "" {
						addr = "https://" + e.Domain
					}
					tm.Apps = append(tm.Apps, env{App: e.ProjectName, Environment: e.Environment, Builds: e.Builds, Address: addr, Status: e.Status})
				}
				out = append(out, tm)
			}
			return result(map[string]any{"signed_in_as": who.User.Email, "teams": out})
		})

	type deployIn struct {
		where
		Run       string            `json:"run,omitempty" jsonschema:"how the app starts, for this deploy (its start command's args)"`
		Release   string            `json:"release,omitempty" jsonschema:"the release command, run before it goes live (migrations)"`
		Processes map[string]string `json:"processes,omitempty" jsonschema:"processes beside the web, name to command; replaces the app's"`
		Save      bool              `json:"save,omitempty" jsonschema:"keep these changes as the app's settings for every later deploy"`
		Wait      *bool             `json:"wait,omitempty" jsonschema:"wait until it's live (default true)"`
		Confirm   bool              `json:"confirm" jsonschema:"true once the person agreed to deploy"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "deploy", Annotations: changes("Deploy"),
		Description: "Deploys an environment - production unless another is named - from the working tree of a folder on this computer (what git would commit, uncommitted changes included): homeport builds it and releases it, replacing what runs there now. run, release and processes change this deploy alone unless save is true, which keeps them for every later deploy. Needs confirm: true. Answers the address once live, the build's id, and the end of its log."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deployIn) (*mcp.CallToolResult, any, error) {
			if err := write(in.Confirm); err != nil {
				return refused(err)
			}
			args := []string{}
			for k, v := range map[string]string{"--team": in.Team, "--app": in.App, "--env": in.Environment, "--run": in.Run, "--release": in.Release} {
				if v != "" {
					args = append(args, k, v)
				}
			}
			for name, cmd := range in.Processes {
				args = append(args, "--process", name+"="+cmd)
			}
			if in.Save {
				args = append(args, "--save")
			}
			if in.Wait != nil && !*in.Wait {
				args = append(args, "--detach")
			}
			var out bytes.Buffer
			err := sub(&out, in.Directory).deploy(ctx, args)
			log := out.String()
			ans := map[string]any{"status": "live", "log": tail(log, 4000)}
			if m := buildPage.FindStringSubmatch(log); m != nil {
				ans["build"], ans["build_page"] = m[2], m[1]
			}
			if m := liveAt.FindStringSubmatch(log); m != nil {
				ans["url"] = m[1]
			}
			var e *exit
			switch {
			case err == nil && in.Wait != nil && !*in.Wait:
				ans["status"] = "building"
			case errors.As(err, &e) && e.code == exitBuildFailed:
				ans["status"], ans["error"] = "build failed", err.Error()
			case errors.As(err, &e) && e.code == exitDeployFailed:
				ans["status"], ans["error"] = "release failed", err.Error()
			case errors.As(err, &e) && e.code == exitTimeout:
				ans["status"], ans["error"] = "still going", err.Error()
			case err != nil:
				return refused(err)
			}
			res, _, jerr := result(ans)
			if jerr == nil && err != nil {
				res.IsError = true
			}
			return res, nil, jerr
		})

	type statusIn struct {
		Team  string `json:"team" jsonschema:"the team's address (slug)"`
		Build string `json:"build" jsonschema:"the build's id, as deploy answered it"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "deploy_status", Annotations: readOnly,
		Description: "Says where a deploy got to: its build's status and the end of its log, then its release's status and detail. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
			c, err := a.client()
			if err != nil {
				return refused(err)
			}
			team, err := teamID(ctx, c, in.Team)
			if err != nil {
				return refused(err)
			}
			b, err := c.Build(ctx, team, in.Build)
			if err != nil {
				return refused(err)
			}
			ans := map[string]any{"build": b.Status, "reason": b.Reason, "log": tail(b.Log, 4000)}
			if b.Deploy != "" {
				d, err := c.DeployStatus(ctx, team, b.Deploy)
				if err != nil {
					return refused(err)
				}
				ans["release"], ans["detail"] = d.Status, d.Detail
			}
			return result(ans)
		})

	type logsIn struct {
		where
		Cursor string `json:"cursor,omitempty" jsonschema:"read on from the cursor the last call answered"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "runtime_logs", Annotations: readOnly,
		Description: "Reads an environment's runtime logs (what the app printed): the newest lines, or those since cursor. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, any, error) {
			c, t, err := at(ctx, in.where)
			if err != nil {
				return refused(err)
			}
			logs, err := c.Logs(ctx, t.link.Team, t.link.App, in.Cursor)
			if err != nil {
				return refused(err)
			}
			return result(logs)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_variables", Annotations: readOnly,
		Description: "Lists an environment's variables by name. Values are never returned, by this or any tool. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in where) (*mcp.CallToolResult, any, error) {
			c, t, err := at(ctx, in)
			if err != nil {
				return refused(err)
			}
			names, err := c.EnvNames(ctx, t.link.Team, t.link.App)
			if err != nil {
				return refused(err)
			}
			return result(map[string]any{"environment": t.link.Environment, "names": names})
		})

	type setIn struct {
		where
		Set     map[string]string `json:"set,omitempty" jsonschema:"variables to set, name to value"`
		Unset   []string          `json:"unset,omitempty" jsonschema:"variables to remove, by name"`
		Confirm bool              `json:"confirm" jsonschema:"true once the person agreed to this change"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "set_variables", Annotations: changes("Set variables"),
		Description: "Sets and removes an environment's variables - production unless another is named. The running app gets them on its next start, which this may cause. Needs confirm: true. Answers the names it has after; values never come back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in setIn) (*mcp.CallToolResult, any, error) {
			if err := write(in.Confirm); err != nil {
				return refused(err)
			}
			c, t, err := at(ctx, in.where)
			if err != nil {
				return refused(err)
			}
			names, err := c.SetEnv(ctx, t.link.Team, t.link.App, in.Set, in.Unset)
			if err != nil {
				return refused(err)
			}
			return result(map[string]any{"environment": t.link.Environment, "names": names})
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_database", Annotations: readOnly,
		Description: "Shows an environment's database - attached or not, its status, the team's databases it could attach - and never its password. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in where) (*mcp.CallToolResult, any, error) {
			c, t, err := at(ctx, in)
			if err != nil {
				return refused(err)
			}
			db, err := c.Database(ctx, t.link.Team, t.link.App)
			if err != nil {
				return refused(err)
			}
			return result(db)
		})

	type confirmIn struct {
		where
		Confirm bool `json:"confirm" jsonschema:"true once the person agreed to this change"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "create_database", Annotations: changes("Create a database"),
		Description: "Makes an environment - production unless another is named - a Postgres database and attaches it: its DATABASE_URL variables are set, and the plan may bill for it. Needs confirm: true."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in confirmIn) (*mcp.CallToolResult, any, error) {
			if err := write(in.Confirm); err != nil {
				return refused(err)
			}
			c, t, err := at(ctx, in.where)
			if err != nil {
				return refused(err)
			}
			db, err := c.CreateDatabase(ctx, t.link.Team, t.link.App)
			if err != nil {
				return refused(err)
			}
			return result(db)
		})

	type attachIn struct {
		where
		Database string `json:"database" jsonschema:"the database's id, from get_database's attachable ones"`
		Confirm  bool   `json:"confirm" jsonschema:"true once the person agreed to this change"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "attach_database", Annotations: changes("Attach a database"),
		Description: "Attaches one of the team's databases to an environment - production unless another is named - setting its DATABASE_URL variables: the app then reads and writes that database. Needs confirm: true."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in attachIn) (*mcp.CallToolResult, any, error) {
			if err := write(in.Confirm); err != nil {
				return refused(err)
			}
			c, t, err := at(ctx, in.where)
			if err != nil {
				return refused(err)
			}
			out, err := c.AttachDatabase(ctx, t.link.Team, t.link.App, in.Database)
			if err != nil {
				return refused(err)
			}
			return result(out)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "detach_database", Annotations: changes("Detach the database"),
		Description: "Detaches an environment's database - production unless another is named: its DATABASE_URL variables are removed and the app loses its database until one is attached. The database itself is kept. Needs confirm: true."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in confirmIn) (*mcp.CallToolResult, any, error) {
			if err := write(in.Confirm); err != nil {
				return refused(err)
			}
			c, t, err := at(ctx, in.where)
			if err != nil {
				return refused(err)
			}
			out, err := c.DetachDatabase(ctx, t.link.Team, t.link.App)
			if err != nil {
				return refused(err)
			}
			return result(out)
		})

	type teamIn struct {
		Team string `json:"team" jsonschema:"the team's address (slug)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "usage", Annotations: readOnly,
		Description: "Shows a team's usage this month - by app and environment - its plan, budget, and what the month is likely to cost. Changes nothing."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in teamIn) (*mcp.CallToolResult, any, error) {
			c, err := a.client()
			if err != nil {
				return refused(err)
			}
			team, err := teamID(ctx, c, in.Team)
			if err != nil {
				return refused(err)
			}
			u, err := c.Usage(ctx, team)
			if err != nil {
				return refused(err)
			}
			return result(u)
		})
	return s
}

var (
	buildPage = regexp.MustCompile(`==> building: (\S+/builds/(\S+))`)
	liveAt    = regexp.MustCompile(`Live: (https://\S+)`)
)

// teamID is the id of the person's team at slug.
func teamID(ctx context.Context, c *cloud.Client, slug string) (string, error) {
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return "", err
	}
	for _, t := range who.Teams {
		if t.Slug == slug || t.ID == slug {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("you aren't in a team %q", slug)
}

// tail is the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
