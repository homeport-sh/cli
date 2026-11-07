package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/homeport-sh/cli/internal/cloud"
)

func logLine(at time.Time, level, process, message string) cloud.LogLine {
	return cloud.LogLine{Time: at, Level: level, Process: process, Message: message}
}

// `homeport logs` prints the environment's newest lines, oldest first:
// when, the process (the web's goes unsaid, as on the dashboard), what.
func TestLogsPrintsTheNewestLines(t *testing.T) {
	f, h := linkedHarness(t)
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	f.logPages = []cloud.Logs{{Cursor: "c1", Lines: []cloud.LogLine{
		logLine(at, "info", "web", "listening on :8080"),
		logLine(at.Add(time.Second), "error", "worker", "panic: boom"),
	}}}
	h.slept = nil // signing in's
	if code := h.run("logs"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	out := h.out.String()
	for _, want := range []string{"12:00:00", "listening on :8080", "12:00:01", "worker", "panic: boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("didn't say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "web") {
		t.Errorf("named the web process:\n%s", out)
	}
	if strings.Index(out, "listening") > strings.Index(out, "panic") {
		t.Errorf("not oldest first:\n%s", out)
	}
	if got := strings.Join(f.logReads, "|"); got != blogID+" " {
		t.Errorf("reads %s", got)
	}
	if len(h.slept) != 0 {
		t.Errorf("waited without --follow: %v", h.slept)
	}

	// nothing yet: it says so, on stderr
	f.logPages, f.logReads = []cloud.Logs{{Cursor: "", Lines: []cloud.LogLine{}}}, nil
	if code := h.run("logs"); code != 0 || h.out.Len() != 0 || !strings.Contains(h.err.String(), "alice/blog (production)") {
		t.Fatalf("none: %d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

// --env reads another environment of the linked app; --process and
// --errors narrow what's printed, as the dashboard's panel does.
func TestLogsNarrows(t *testing.T) {
	f, h := linkedHarness(t)
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	page := cloud.Logs{Cursor: "c1", Lines: []cloud.LogLine{
		logLine(at, "info", "web", "GET /"),
		logLine(at, "error", "web", "web failed"),
		logLine(at, "warning", "worker", "worker slow"),
		logLine(at, "error", "worker", "worker failed"),
	}}
	f.logPages = []cloud.Logs{page, page, page}
	if code := h.run("logs", "--env", "staging", "--process", "worker"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if out := h.out.String(); strings.Contains(out, "GET /") || strings.Contains(out, "web failed") || !strings.Contains(out, "worker slow") || !strings.Contains(out, "worker failed") {
		t.Errorf("--process worker:\n%s", out)
	}
	if code := h.run("logs", "--errors"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if out := h.out.String(); strings.Contains(out, "GET /") || strings.Contains(out, "worker slow") || !strings.Contains(out, "web failed") || !strings.Contains(out, "worker failed") {
		t.Errorf("--errors:\n%s", out)
	}
	if code := h.run("logs", "--process", "web", "--errors"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if out := h.out.String(); strings.Count(out, "\n") != 1 || !strings.Contains(out, "web failed") {
		t.Errorf("--process web --errors:\n%s", out)
	}
	if got := strings.Join(f.logReads, "|"); got != stageID+" |"+blogID+" |"+blogID+" " {
		t.Errorf("reads %s", got)
	}
	if code := h.run("logs", "extra"); code != exitUsage {
		t.Errorf("an argument: %d %s", code, h.said())
	}
}

// --follow (-f) reads on from each cursor every 2s, as the dashboard's
// panel does, printing what's new, until it's stopped (Ctrl-C): that ends
// it cleanly, exit 0.
func TestLogsFollowReadsOnFromTheCursor(t *testing.T) {
	for _, flag := range []string{"--follow", "-f"} {
		f, h := linkedHarness(t)
		at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
		f.logPages = []cloud.Logs{
			{Cursor: "c1", Lines: []cloud.LogLine{logLine(at, "info", "web", "first")}},
			{Cursor: "c1", Lines: []cloud.LogLine{}},
			{Cursor: "c2", Lines: []cloud.LogLine{logLine(at.Add(time.Second), "info", "web", "second")}},
		}
		h.slept = nil // signing in's
		h.a.sleep = func(ctx context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			if len(h.slept) == 4 {
				return context.Canceled // Ctrl-C while waiting
			}
			return nil
		}
		if code := h.run("logs", flag); code != 0 {
			t.Fatalf("%s: %d %s", flag, code, h.said())
		}
		out := h.out.String()
		if strings.Count(out, "first") != 1 || strings.Count(out, "second") != 1 || strings.Index(out, "first") > strings.Index(out, "second") {
			t.Errorf("%s printed:\n%s", flag, out)
		}
		if got := strings.Join(f.logReads, "|"); got != blogID+" |"+blogID+" c1|"+blogID+" c1|"+blogID+" c2" {
			t.Errorf("%s reads %s", flag, got)
		}
		for _, d := range h.slept {
			if d != 2*time.Second {
				t.Errorf("%s waited %v", flag, d)
			}
		}
		if strings.Contains(h.err.String(), "error") {
			t.Errorf("%s: stopping said %s", flag, h.err.String())
		}
	}
}

// A refusal - too many log panels open, say - is the API's own words.
func TestLogsRefusedSaysWhy(t *testing.T) {
	f, h := linkedHarness(t)
	f.logRefuse = &cloud.Error{Status: http.StatusTooManyRequests, Message: "too many log panels reading at once: close one, or wait a moment"}
	if code := h.run("logs", "-f"); code != exitError || !strings.Contains(h.said(), "too many log panels") {
		t.Fatalf("%d %s", code, h.said())
	}
}
