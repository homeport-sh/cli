package buildplan_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/homeport-sh/cli/buildplan"
)

// A server whose fingerprinted files ship inside it - a compiled binary,
// rsc-kit's - names their folder and URL path in the plan (assets), for
// the builder to keep them beside it: the release after it answers a tab
// opened before that deploy with them.
func TestAServersFingerprintedFilesAreNamed(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		root  string
		want  []buildplan.PlanAsset
	}{
		"rsc-kit": {map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, "",
			[]buildplan.PlanAsset{{From: ".output/public/assets", At: "/assets"}}},
		"rsc-kit in a folder": {map[string]string{"web/package.json": rscKitPkg, "web/bun.lock": bunLock}, "web",
			[]buildplan.PlanAsset{{From: ".output/public/assets", At: "/assets"}}},
		"next-bun-compile": {js(`{"scripts":{"build":"NEXT_ADAPTER_PATH=next-bun-compile next build"},"dependencies":{"next":"16","next-bun-compile":"^2"}}`, "bun.lock", "{}"), "",
			[]buildplan.PlanAsset{{From: ".next/static", At: "/_next/static"}}},
		"Nuxt, compiled": {js(`{"scripts":{"build":"nuxt build --preset bun && bun build --compile --production .output/server/index.mjs --outfile server"},"dependencies":{"nuxt":"^4"}}`, "bun.lock", "{}",
			"nuxt.config.ts", "export default defineNuxtConfig({ nitro: { serveStatic: 'inline' } })\n"), "",
			[]buildplan.PlanAsset{{From: ".output/public/_nuxt", At: "/_nuxt"}}},
	} {
		p := detect(t, c.files, buildplan.Settings{Root: c.root})
		if !reflect.DeepEqual(p.Assets, c.want) {
			t.Errorf("%s: %+v", name, p.Assets)
		}
	}
	// the builder reads them as assets in the plan's JSON
	p := detect(t, map[string]string{"package.json": rscKitPkg, "bun.lock": bunLock}, buildplan.Settings{})
	if b, _ := json.Marshal(p); !strings.Contains(string(b), `"assets":[{"from":".output/public/assets","at":"/assets"}]`) {
		t.Errorf("json: %s", b)
	}
	// a bundle with a tree in .homeport/public has none to name: the next
	// release keeps its immutable files already
	p = detect(t, js(`{"scripts":{"build":"next build"},"dependencies":{"next":"16"}}`, "next.config.ts", "export default { output: \"standalone\" }\n"), buildplan.Settings{})
	if p.Assets != nil {
		t.Errorf("a standalone Next.js: %+v", p.Assets)
	}
	if b, _ := json.Marshal(p); strings.Contains(string(b), `"assets"`) {
		t.Errorf("json without any: %s", b)
	}
}
