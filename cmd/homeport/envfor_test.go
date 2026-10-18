package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// --for sends a value for one role alone (design/variable-roles.md): never
// in the value for all, and said without the value.
func TestEnvSetForOneRole(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "MIGRATION_DATABASE_URL=postgres://ddl:s3cret@db/x", "--for", "release"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if _, ok := f.env["MIGRATION_DATABASE_URL"]; ok {
		t.Fatalf("sent as the value for all: %v", f.env)
	}
	if got := f.rolesSent["release"].Set["MIGRATION_DATABASE_URL"]; got != "postgres://ddl:s3cret@db/x" {
		t.Fatalf("roles sent %+v", f.rolesSent)
	}
	said := h.said()
	if strings.Contains(said, "s3cret") || !strings.Contains(said, "MIGRATION_DATABASE_URL for the release command") || !strings.Contains(said, "next deploy") {
		t.Fatalf("said %s", said)
	}
}

func TestEnvSetForSeveralRoles(t *testing.T) {
	f, h := linkedHarness(t)
	if code := h.run("env", "set", "DATABASE_POOL_MAX=4", "--for", "worker", "--for", "scheduler"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if got := slices.Sorted(maps.Keys(f.rolesSent)); !slices.Equal(got, []string{"scheduler", "worker"}) ||
		f.rolesSent["worker"].Set["DATABASE_POOL_MAX"] != "4" || f.rolesSent["scheduler"].Set["DATABASE_POOL_MAX"] != "4" {
		t.Fatalf("roles sent %+v", f.rolesSent)
	}
	if len(f.env) != 0 {
		t.Fatalf("sent for all: %v", f.env)
	}
	if said := h.said(); !strings.Contains(said, "scheduler, worker") || !strings.Contains(said, "restart") {
		t.Fatalf("said %s", said)
	}
}

func TestEnvUnsetForOneRole(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"DATABASE_POOL_MAX": "20"}
	if code := h.run("env", "unset", "DATABASE_POOL_MAX", "--for", "worker"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	if f.env["DATABASE_POOL_MAX"] != "20" {
		t.Fatalf("the value for all went: %v", f.env)
	}
	if !slices.Equal(f.rolesSent["worker"].Unset, []string{"DATABASE_POOL_MAX"}) {
		t.Fatalf("roles sent %+v", f.rolesSent)
	}
	if said := h.said(); !strings.Contains(said, "worker's value of DATABASE_POOL_MAX") {
		t.Fatalf("said %s", said)
	}
}

func TestEnvForRefused(t *testing.T) {
	for _, args := range [][]string{
		{"set", "A=1", "--for", "release", "--build"},
		{"set", "A=1", "--for", "release", "--build-only"},
		{"set", "A=1", "--for", "release", "--runtime"},
		{"set", "A", "--for", "release", "--from-file", "x.pem"},
		{"set", "A", "--for", "release"},
		{"set", "A=1", "--for", "job"},
		{"set", "A=1", "--for", "Web"},
		{"set", "A=1", "--for", "averyveryverylongname"},
		{"unset", "A", "--for", "job"},
	} {
		f, h := linkedHarness(t)
		if code := h.run(append([]string{"env"}, args...)...); code == 0 {
			t.Fatalf("%v took: %s", args, h.said())
		}
		if f.envPosts != 0 {
			t.Fatalf("%v sent", args)
		}
	}
}

// env ls says, per name, which parts of the app get it: all, one role's
// alone, or all with some role's own.
func TestEnvLsSaysForWhom(t *testing.T) {
	f, h := linkedHarness(t)
	f.env = map[string]string{"DATABASE_POOL_MAX": "20", "MIGRATION_DATABASE_URL": "", "NEXT_PUBLIC_API_URL": "x"}
	f.roles = map[string][]string{"DATABASE_POOL_MAX": {"worker"}, "MIGRATION_DATABASE_URL": {"release"}}
	f.rolesOnly = map[string]bool{"MIGRATION_DATABASE_URL": true}
	if code := h.run("env", "ls"); code != 0 {
		t.Fatalf("%d %s", code, h.said())
	}
	lines := strings.Split(h.said(), "\n")
	if !strings.Contains(lines[0], "FOR") {
		t.Fatalf("header %q", lines[0])
	}
	for name, want := range map[string]string{
		"DATABASE_POOL_MAX":      "all; own: worker",
		"MIGRATION_DATABASE_URL": "release",
		"NEXT_PUBLIC_API_URL":    "all",
	} {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, name+" ") {
				found = true
				if !strings.Contains(l, "  "+want+"  ") {
					t.Fatalf("%s: %q, want %q", name, l, want)
				}
			}
		}
		if !found {
			t.Fatalf("no row for %s: %s", name, h.said())
		}
	}
}
