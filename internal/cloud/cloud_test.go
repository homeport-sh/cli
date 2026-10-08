package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/buildplan"
)

func TestCallsCarryTheTokenAndSayWhoTheyAre(t *testing.T) {
	var auth, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, agent = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		io.WriteString(w, `{"user":{"id":"u1","name":"Alice","email":"a@example.com","login":"alice"},"token":{"id":"t1","name":"laptop"},
			"teams":[{"ID":"team1","Name":"alice","Slug":"alice","Role":"owner"}],"dashboard":"https://app.homeport.test"}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "hpcli_secret", UserAgent: "homeport/1.2.3"}
	who, err := c.WhoAmI(context.Background())
	if err != nil || who.User.Email != "a@example.com" || who.Teams[0].Slug != "alice" || who.Dashboard != "https://app.homeport.test" {
		t.Fatalf("%+v %v", who, err)
	}
	if auth != "Bearer hpcli_secret" || agent != "homeport/1.2.3" {
		t.Fatalf("auth %q agent %q", auth, agent)
	}
}

func TestRefusalsSayWhatTheyMean(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "hpcli_secret"}

	status, body = 401, `{"error":"not signed in"}`
	if _, err := c.WhoAmI(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("401: %v", err)
	}
	status, body = 400, `{"error":"a deploy of its own can only change settings","field":"build"}`
	_, err := c.Deploy(context.Background(), "t", "a", DeployRequest{Source: "s", SHA: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Status != 400 || e.Field != "build" || !strings.Contains(err.Error(), "only change settings") {
		t.Fatalf("400: %v", err)
	}
	status, body = 500, `<html>oops</html>`
	if _, err := c.WhoAmI(context.Background()); err == nil || strings.Contains(err.Error(), "html") {
		t.Fatalf("500: %v", err)
	}
	// the token is never in what an error says
	for _, s := range []int{400, 401, 404, 500} {
		status, body = s, `{"error":"no"}`
		if _, err := c.WhoAmI(context.Background()); err == nil || strings.Contains(err.Error(), "hpcli_secret") {
			t.Fatalf("%d: %v", s, err)
		}
	}
}

func TestDeployingSendsTheChangeAsTheDashboardTakesIt(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cli/teams/team1/apps/app1/deploys" || r.Method != "POST" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"build":"b1"}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "t"}
	b, err := c.Deploy(context.Background(), "team1", "app1", DeployRequest{Source: "s1", SHA: "abc",
		Settings: buildplan.Settings{Run: "./server", Processes: []buildplan.Process{{Name: "worker", Run: "./work"}}}, Save: true})
	if err != nil || b != "b1" {
		t.Fatalf("%q %v", b, err)
	}
	settings, _ := got["settings"].(map[string]any)
	if got["source"] != "s1" || got["save"] != true || settings["run"] != "./server" {
		t.Fatalf("%v", got)
	}
}

func TestUploadingPutsExactlyTheSizeAsked(t *testing.T) {
	var length int64
	var ctype, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		length, ctype, auth = r.ContentLength, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()
	c := &Client{Base: "https://api.example", Token: "hpcli_secret"}
	if err := c.Upload(context.Background(), srv.URL+"/put", strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}
	// the bucket's link is signed for this: no token goes to it
	if length != 5 || ctype != "application/gzip" || auth != "" {
		t.Fatalf("length %d type %q auth %q", length, ctype, auth)
	}
}

// Queueing a deploy waits for the upload to be checked: longer than a read.
func TestADeployWaitsLongerThanARead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, `{"build":"b1","user":{}}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "t", Timeout: 50 * time.Millisecond}
	if _, err := c.WhoAmI(context.Background()); err == nil {
		t.Fatal("a read outlived its timeout")
	}
	if b, err := c.Deploy(context.Background(), "t", "a", DeployRequest{}); err != nil || b != "b1" {
		t.Fatalf("deploy %q %v", b, err)
	}
}

func TestTheRestOfTheTokenAPI(t *testing.T) {
	var calls []string
	var setBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case strings.HasSuffix(r.URL.Path, "/env") && r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&setBody)
			io.WriteString(w, `["A","B"]`)
		case strings.HasSuffix(r.URL.Path, "/env"):
			io.WriteString(w, `{"Names":["A","B"]}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			io.WriteString(w, `{"Cursor":"c2","Lines":[{"Time":"2026-10-08T12:00:00Z","Level":"info","Message":"hi","Process":"web"}]}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "t"}
	ctx := context.Background()
	if names, err := c.EnvNames(ctx, "t1", "a1"); err != nil || strings.Join(names, ",") != "A,B" {
		t.Fatalf("names %v %v", names, err)
	}
	if names, err := c.SetEnv(ctx, "t1", "a1", map[string]string{"A": "1"}, []string{"C"}); err != nil || len(names) != 2 {
		t.Fatalf("set %v %v", names, err)
	}
	if set, _ := setBody["set"].(map[string]any); set["A"] != "1" {
		t.Fatalf("sent %v", setBody)
	}
	logs, err := c.Logs(ctx, "t1", "a1", "c1")
	if err != nil || logs.Cursor != "c2" || logs.Lines[0].Message != "hi" {
		t.Fatalf("logs %+v %v", logs, err)
	}
	for _, f := range []func() (json.RawMessage, error){
		func() (json.RawMessage, error) { return c.Database(ctx, "t1", "a1") },
		func() (json.RawMessage, error) { return c.CreateDatabase(ctx, "t1", "a1") },
		func() (json.RawMessage, error) { return c.AttachDatabase(ctx, "t1", "a1", "db1") },
		func() (json.RawMessage, error) { return c.DetachDatabase(ctx, "t1", "a1") },
		func() (json.RawMessage, error) { return c.Usage(ctx, "t1") },
	} {
		if out, err := f(); err != nil || len(out) == 0 {
			t.Fatalf("%s %v", out, err)
		}
	}
	want := []string{"GET /v1/cli/teams/t1/apps/a1/env", "POST /v1/cli/teams/t1/apps/a1/env", "GET /v1/cli/teams/t1/apps/a1/logs?cursor=c1",
		"GET /v1/cli/teams/t1/apps/a1/database", "POST /v1/cli/teams/t1/apps/a1/database", "POST /v1/cli/teams/t1/apps/a1/database/attach",
		"POST /v1/cli/teams/t1/apps/a1/database/detach", "GET /v1/cli/teams/t1/usage"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls\n%s", strings.Join(calls, "\n"))
	}
}
