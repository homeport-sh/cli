package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSession is an editor connected to `homeport mcp`, in memory.
func mcpSession(t *testing.T, h *harness) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := h.a.mcpServer()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "editor", Version: "1"}, nil).Connect(ctx, ct, nil)
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

func TestTheMCPToolsSayWhatTheyChange(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	cs := mcpSession(t, h)
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
	}
	reads := []string{"list_apps", "deploy_status", "runtime_logs", "list_variables", "get_database", "usage"}
	writes := []string{"deploy", "set_variables", "create_database", "attach_database", "detach_database"}
	for _, name := range reads {
		if tool := got[name]; tool == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: not a read-only tool (%v)", name, tool)
		}
	}
	for _, name := range writes {
		tool := got[name]
		if tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: not marked as changing things (%v)", name, tool)
			continue
		}
		if !strings.Contains(tool.Description, "production") {
			t.Errorf("%s doesn't say it can change production: %s", name, tool.Description)
		}
	}
	if len(got) != len(reads)+len(writes) {
		t.Errorf("tools %v", got)
	}
}

func TestVariablesGoInAndOnlyTheirNamesComeOut(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	cs := mcpSession(t, h)
	at := map[string]any{"team": "alice", "app": "blog", "environment": "production"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range at {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	// not confirmed: nothing changes
	if text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"API_KEY": "s3cret"}})); !isErr || !strings.Contains(text, "confirm") {
		t.Fatalf("unconfirmed: %v %s", isErr, text)
	}
	if len(f.env) != 0 {
		t.Fatal("changed without confirming")
	}
	text, isErr := callTool(t, cs, "set_variables", with(map[string]any{"set": map[string]string{"API_KEY": "s3cret"}, "confirm": true}))
	if isErr || strings.Contains(text, "s3cret") || !strings.Contains(text, "API_KEY") {
		t.Fatalf("set: %v %s", isErr, text)
	}
	if f.env["API_KEY"] != "s3cret" {
		t.Fatalf("env %v", f.env)
	}
	if text, isErr := callTool(t, cs, "list_variables", at); isErr || strings.Contains(text, "s3cret") || !strings.Contains(text, "API_KEY") {
		t.Fatalf("list: %v %s", isErr, text)
	}
	if text, _ := callTool(t, cs, "runtime_logs", at); !strings.Contains(text, "panic: boom") {
		t.Fatalf("logs %s", text)
	}
	if text, _ := callTool(t, cs, "usage", map[string]any{"team": "alice"}); !strings.Contains(text, "900") {
		t.Fatalf("usage %s", text)
	}
	if _, isErr := callTool(t, cs, "create_database", with(map[string]any{"confirm": true})); isErr || len(f.dbCalls) != 1 || f.dbCalls[0] != "POST /database" {
		t.Fatalf("create %v %v", isErr, f.dbCalls)
	}
	if _, isErr := callTool(t, cs, "detach_database", with(nil)); !isErr || len(f.dbCalls) != 1 {
		t.Fatalf("detach unconfirmed %v %v", isErr, f.dbCalls)
	}
}

func TestTheMCPDeploysAndSaysWhereItsLive(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	project(t, h)
	cs := mcpSession(t, h)
	text, isErr := callTool(t, cs, "deploy", map[string]any{"directory": h.a.wd, "run": "./server --debug", "confirm": true})
	if isErr {
		t.Fatalf("deploy: %s", text)
	}
	var out struct{ URL, Status, Build string }
	if err := json.Unmarshal([]byte(text[:strings.LastIndex(text, "}")+1]), &out); err != nil || out.URL != "https://blog.homeport.app" || out.Status != "live" || out.Build != "b1" {
		t.Fatalf("%s (%v)", text, err)
	}
	if f.deploys[0].Settings.Run != "./server --debug" {
		t.Fatalf("%+v", f.deploys[0])
	}
	if text, _ := callTool(t, cs, "deploy_status", map[string]any{"team": "alice", "build": "b1"}); !strings.Contains(text, "succeeded") {
		t.Fatalf("status %s", text)
	}
}

func TestTheMCPSaysToSignIn(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	cs := mcpSession(t, h)
	if text, isErr := callTool(t, cs, "list_apps", nil); !isErr || !strings.Contains(text, "homeport login") {
		t.Fatalf("%v %s", isErr, text)
	}
}

func TestTheMCPLimitsHowFastItChangesThings(t *testing.T) {
	f := newFakeAPI(t)
	h := newHarness(t, f)
	h.login(t)
	cs := mcpSession(t, h)
	args := map[string]any{"team": "alice", "app": "blog", "unset": []string{"X"}, "confirm": true}
	limited := false
	for range mcpWritesPerMinute + 1 {
		if text, isErr := callTool(t, cs, "set_variables", args); isErr && strings.Contains(text, "too many") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("never limited")
	}
}
