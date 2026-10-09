package source

import "testing"

func TestIgnorePatterns(t *testing.T) {
	cases := []struct {
		patterns string
		path     string
		dir      bool
		want     bool
	}{
		{"node_modules", "node_modules", true, true},
		{"node_modules", "web/node_modules", true, true},
		{"node_modules/", "node_modules", false, false}, // a file of that name isn't the folder
		{"*.log", "logs/app.log", false, true},
		{"*.log", "app.logs", false, false},
		{"/build", "build", true, true},
		{"/build", "web/build", true, false}, // anchored to its .gitignore's folder
		{"dist/out", "dist/out", false, true},
		{"dist/out", "web/dist/out", false, false}, // a slash inside anchors it too
		{"**/cache", "a/b/cache", true, true},
		{"logs/**", "logs/a/b.txt", false, true},
		{"a/**/b", "a/b", false, true},
		{"a/**/b", "a/x/y/b", false, true},
		{"*.env\n!.env.example", ".env.example", false, false},
		{"*.env\n!.env.example", "prod.env", false, true},
		{"# a comment\n\n", "anything", false, false},
		{"\\#hash", "#hash", false, true},
		{"file?.txt", "file1.txt", false, true},
		{"file[0-9].txt", "filex.txt", false, false},
		{"file[0-9].txt", "file7.txt", false, true},
		{".env*  ", ".env.local", false, true}, // trailing spaces aren't part of it
	}
	for _, c := range cases {
		m := &ignorer{}
		m.add("", c.patterns)
		if got := m.ignored(c.path, c.dir); got != c.want {
			t.Errorf("%q against %q (dir %v): %v, want %v", c.patterns, c.path, c.dir, got, c.want)
		}
	}
}

// A .gitignore in a folder speaks for that folder, below the root's.
func TestANestedGitignoreIsRelativeToItsFolder(t *testing.T) {
	m := &ignorer{}
	m.add("", "*.tmp")
	m.add("web", "/public/build\n!keep.tmp")
	for path, want := range map[string]bool{
		"web/public/build": true,
		"public/build":     false,
		"web/keep.tmp":     false, // re-included below
		"keep.tmp":         true,
		"web/x.tmp":        true,
	} {
		if got := m.ignored(path, false); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
