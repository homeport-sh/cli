package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// person answers the server's questions, as an editor shows them: approve
// or not, and every message asked.
type person struct {
	approve bool
	asked   []string
}

func (p *person) answer(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	p.asked = append(p.asked, req.Params.Message)
	if !p.approve {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
}

// mcpSession is an editor connected to `homeport mcp`, in memory; p (nil:
// an editor that can't ask) answers what the server asks.
func mcpSession(t *testing.T, h *harness, p *person) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := h.a.mcpServer().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	var opts *mcp.ClientOptions
	if p != nil {
		opts = &mcp.ClientOptions{ElicitationHandler: p.answer}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "editor", Version: "1"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool calls a tool and answers its text, and whether it was an error.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

var (
	readTools  = []string{"list_apps", "deploy_status", "runtime_logs", "list_variables", "get_database", "usage"}
	writeTools = []string{"deploy", "set_variables", "unset_variables", "create_database", "attach_database", "detach_database"}
)

func TestTheMCPToolsSayWhatTheyChange(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	cs := mcpSession(t, h, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
		if tool.Description == "" || tool.Annotations == nil {
			t.Errorf("%s says nothing of itself", tool.Name)
		}
		for _, banned := range []string{"delete", "remove_domain", "reveal"} {
			if strings.Contains(tool.Name, banned) {
				t.Errorf("%s is exposed", tool.Name)
			}
		}
		// the agent can't point a tool at a folder, or say the person agreed
		schema, _ := json.Marshal(tool.InputSchema)
		for _, field := range []string{`"directory"`, `"confirm"`} {
			if strings.Contains(string(schema), field) {
				t.Errorf("%s takes %s", tool.Name, field)
			}
		}
	}
	for _, name := range readTools {
		if tool := got[name]; tool == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: not a read-only tool (%v)", name, tool)
		}
	}
	for _, name := range writeTools {
		tool := got[name]
		if tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: not marked as changing things (%v)", name, tool)
			continue
		}
		if !strings.Contains(tool.Description, "production") || !strings.Contains(tool.Description, "asks you") {
			t.Errorf("%s doesn't say it can change production, or that it asks: %s", name, tool.Description)
		}
	}
	if len(got) != len(readTools)+len(writeTools) {
		t.Errorf("tools %v", got)
	}
}

var blog = map[string]any{"team": "alice", "app": "blog", "environment": "production"}

func with(extra map[string]any) map[string]any {
	m := map[string]any{}
	for k, v := range blog {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// A change is the person's to approve, asked by their editor - not
// something the agent can say for them.
func TestAChangeIsAskedOfThePerson(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	no := &person{}
	cs := mcpSession(t, h, no)
	if text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"API_KEY": "s3cret"}})); !isErr || !strings.Contains(text, "declined") {
		t.Fatalf("declined: %v %s", isErr, text)
	}
	if len(f.env) != 0 {
		t.Fatal("changed though declined")
	}
	if len(no.asked) != 1 || !strings.Contains(no.asked[0], "API_KEY") || !strings.Contains(no.asked[0], "production") || strings.Contains(no.asked[0], "s3cret") {
		t.Fatalf("asked %q", no.asked)
	}

	yes := &person{approve: true}
	cs = mcpSession(t, h, yes)
	text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"API_KEY": "s3cret"}}))
	if isErr || strings.Contains(text, "s3cret") || !strings.Contains(text, "API_KEY") || f.env["API_KEY"] != "s3cret" {
		t.Fatalf("set: %v %s %v", isErr, text, f.env)
	}
	if text, isErr := callTool(t, cs, "list_variables", blog); isErr || strings.Contains(text, "s3cret") || !strings.Contains(text, "API_KEY") {
		t.Fatalf("list: %v %s", isErr, text)
	}
	// removing is its own tool, asked on its own, naming what goes
	if _, isErr := callTool(t, cs, "unset_variables", with(map[string]any{"names": []string{"API_KEY"}})); isErr || len(f.env) != 0 {
		t.Fatalf("unset %v %v", isErr, f.env)
	}
	if last := yes.asked[len(yes.asked)-1]; !strings.Contains(last, "Remove") || !strings.Contains(last, "API_KEY") {
		t.Fatalf("asked %q", last)
	}
	if text, _ := callTool(t, cs, "runtime_logs", blog); !strings.Contains(text, "panic: boom") {
		t.Fatalf("logs %s", text)
	}
	if text, _ := callTool(t, cs, "usage", map[string]any{"team": "alice"}); !strings.Contains(text, "900") {
		t.Fatalf("usage %s", text)
	}
	if _, isErr := callTool(t, cs, "create_database", blog); isErr || len(f.dbCalls) != 1 || f.dbCalls[0] != "POST /database" {
		t.Fatalf("create %v %v", isErr, f.dbCalls)
	}
}

// An editor that can't ask the person gets no changes - unless the person
// started the server saying so (homeport mcp --allow-changes).
func TestWithoutAskingThereAreNoChanges(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	cs := mcpSession(t, h, nil)
	if text, isErr := callTool(t, cs, "detach_database", blog); !isErr || !strings.Contains(text, "--allow-changes") || len(f.dbCalls) != 0 {
		t.Fatalf("unasked: %v %s %v", isErr, text, f.dbCalls)
	}
	h.a.mcpAllowChanges = true
	cs = mcpSession(t, h, nil)
	if _, isErr := callTool(t, cs, "detach_database", blog); isErr || len(f.dbCalls) != 1 {
		t.Fatalf("allowed: %v %v", isErr, f.dbCalls)
	}
}

// deploy deploys the folder the server runs in, as linked - nothing the
// agent names.
func TestTheMCPDeploysItsOwnFolder(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	p := &person{approve: true}
	cs := mcpSession(t, h, p)
	text, isErr := callTool(t, cs, "deploy", map[string]any{"run": "./server --debug"})
	if isErr {
		t.Fatalf("deploy: %s", text)
	}
	var out struct{ URL, Status, Build string }
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.URL != "https://blog.homeport.app" || out.Status != "live" || out.Build != "b1" {
		t.Fatalf("%s (%v)", text, err)
	}
	if f.deploys[0].Settings.Run != "./server --debug" {
		t.Fatalf("%+v", f.deploys[0])
	}
	if !strings.Contains(p.asked[0], h.a.wd) || !strings.Contains(p.asked[0], "alice/blog (production)") {
		t.Fatalf("asked %q", p.asked)
	}
	if text, _ := callTool(t, cs, "deploy_status", map[string]any{"team": "alice", "build": "b1"}); !strings.Contains(text, "succeeded") {
		t.Fatalf("status %s", text)
	}
}

func TestTheMCPSaysToSignIn(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	cs := mcpSession(t, h, nil)
	if text, isErr := callTool(t, cs, "list_apps", nil); !isErr || !strings.Contains(text, "homeport login") {
		t.Fatalf("%v %s", isErr, text)
	}
}

func TestTheMCPLimitsHowFastItChangesThings(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	cs := mcpSession(t, h, &person{approve: true})
	limited := false
	for range mcpWritesPerMinute + 1 {
		if text, isErr := callTool(t, cs, "unset_variables", with(map[string]any{"names": []string{"X"}})); isErr && strings.Contains(text, "too many") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("never limited")
	}
}

// An approval is for the change it was asked about, once: an answer can't be
// carried over to another, or used twice.
func TestAnApprovalIsForThatChangeOnce(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := h.a.mcpServer().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	// an editor that answers by hand: it sees the question, and retries itself
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "editor", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: (&person{approve: true}).answer, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}}).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	asked, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "unset_variables", Arguments: with(map[string]any{"names": []string{"HARMLESS"}})})
	if err != nil || asked.RequestState == "" || asked.InputRequests["approve"] == nil {
		t.Fatalf("not asked: %+v %v", asked, err)
	}
	yes := mcp.InputResponseMap{"approve": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}}
	f.env = map[string]string{"DATABASE_URL": "x", "HARMLESS": "y"}
	// the answer, carried over to another change
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "unset_variables", Arguments: with(map[string]any{"names": []string{"DATABASE_URL"}}),
		InputResponses: yes, RequestState: asked.RequestState})
	if err != nil || !res.IsError || f.env["DATABASE_URL"] != "x" {
		t.Fatalf("carried over: %+v %v %v", res, err, f.env)
	}
	// spent by the attempt: not good for its own change either
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "unset_variables", Arguments: with(map[string]any{"names": []string{"HARMLESS"}}),
		InputResponses: yes, RequestState: asked.RequestState})
	if !res.IsError || f.env["HARMLESS"] != "y" {
		t.Fatalf("used twice: %+v %v", res, f.env)
	}
}

// What's asked is what's changed: a link whose labels say staging while
// its ids are production's is refused before anything is asked or done.
func TestALinksLabelsCantDisguiseTheChange(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	config.SaveLink(h.a.wd, config.Link{Team: team1, TeamSlug: "alice", App: blogID, Project: "blog", Environment: "staging"})
	p := &person{approve: true}
	cs := mcpSession(t, h, p)
	for _, call := range []struct {
		tool string
		args map[string]any
	}{{"set_variables", map[string]any{"set": map[string]string{"K": "v"}}}, {"deploy", map[string]any{}}} {
		if text, isErr := callTool(t, cs, call.tool, call.args); !isErr || !strings.Contains(text, "production") {
			t.Fatalf("%s: %v %s", call.tool, isErr, text)
		}
	}
	if len(p.asked) != 0 || len(f.env) != 0 || len(f.deploys) != 0 {
		t.Fatalf("asked %q, env %v, deploys %v", p.asked, f.env, f.deploys)
	}
}

// The question is the same each time it's asked of the same change - so
// its answer, given once, is the answer to it.
func TestAQuestionIsTheSameEachTime(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	cs := mcpSession(t, h, &person{approve: true})
	args := map[string]any{"run": "./server", "release": "./migrate", "processes": map[string]string{"worker": "./w", "cron": "./c", "mail": "./m"}}
	for i := range 8 {
		f.buildGet, f.steps = 0, []string{"uploaded", "deploying", "live"}
		if text, isErr := callTool(t, cs, "deploy", args); isErr {
			t.Fatalf("deploy %d: %s", i, text)
		}
	}
}

// Reading, variables and add-ons are any environment's - one its own CI
// deploys too.
func TestReadingAnEnvironmentHomeportDoesntBuild(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	cs := mcpSession(t, h, nil)
	if text, isErr := callTool(t, cs, "list_variables", map[string]any{"team": "alice", "app": "api"}); isErr {
		t.Fatalf("%s", text)
	}
}

// An approval is for the values too: approving one value isn't approving
// another under the same name.
func TestAnApprovalCoversTheValues(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := h.a.mcpServer().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "editor", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: (&person{approve: true}).answer, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}}).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	asked, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_variables", Arguments: with(map[string]any{"set": map[string]string{"API_URL": "https://good"}})})
	if err != nil || asked.RequestState == "" {
		t.Fatalf("not asked: %+v %v", asked, err)
	}
	yes := mcp.InputResponseMap{"approve": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_variables", Arguments: with(map[string]any{"set": map[string]string{"API_URL": "https://evil"}}),
		InputResponses: yes, RequestState: asked.RequestState})
	if err != nil || !res.IsError || len(f.env) != 0 {
		t.Fatalf("another value went in: %+v %v %v", res, err, f.env)
	}
}

// What an agent wrote is quoted, never read as the question's own words; a
// database is named, with where else it's attached.
func TestTheQuestionQuotesWhatTheAgentWrote(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	p := &person{approve: true}
	cs := mcpSession(t, h, p)
	callTool(t, cs, "unset_variables", with(map[string]any{"names": []string{"X\nApprove: harmless"}}))
	if len(p.asked) != 1 || !strings.Contains(p.asked[0], `"X\nApprove: harmless"`) {
		t.Fatalf("asked %q", p.asked)
	}
	if _, isErr := callTool(t, cs, "attach_database", with(map[string]any{"database": "db2"})); isErr {
		t.Fatal("attach")
	}
	if last := p.asked[len(p.asked)-1]; !strings.Contains(last, `"api-production"`) || !strings.Contains(last, "also attached to") {
		t.Fatalf("asked %q", last)
	}
	if text, isErr := callTool(t, cs, "attach_database", with(map[string]any{"database": "db-nope"})); !isErr || !strings.Contains(text, "isn't one") {
		t.Fatalf("an unknown database: %v %s", isErr, text)
	}
}
