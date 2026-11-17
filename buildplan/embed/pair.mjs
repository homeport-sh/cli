// pair.mjs: what a backend-and-renderer pair's build reads of rsc-kit's
// (backend.go): its marker, .output/rsc-kit.json, says it made a server -
// the renderer - and names the backend's paths, the URL paths the host
// sends straight to the backend beside it. They're written to <out>, a path
// a line, for the builder; none is an empty file.
//
//   bun pair.mjs <out> <backend> [path ...]     (cwd: the app's folder)
//
// Paths given (homeport.yaml's, or the build settings') are written in
// place of the marker's. A path holding the host-call endpoint, which only
// the renderer calls, is refused: the host would send browsers to it. The
// rest of the paths' rules are the host's, which the builder checks.

import { readFileSync, writeFileSync } from "node:fs";

const [out, backend, ...given] = process.argv.slice(2);
const marker = ".output/rsc-kit.json";

function fail(why) {
	console.error("homeport: " + why);
	process.exit(1);
}

let text;
try {
	text = readFileSync(marker, "utf8");
} catch {
	fail("the build made no " + marker + ": the renderer's build is rsc-kit's (its build script), which writes it");
}
let m;
try {
	m = JSON.parse(text);
} catch {
	fail(marker + " isn't JSON");
}
if (m?.output !== "server") {
	fail("rsc-kit made a static export, and a backend beside it needs its server: drop output: \"export\" from its config");
}

let paths = given;
if (!given.length) {
	if (!("backendPaths" in m)) {
		console.error("homeport: rsc-kit " + (m.version ? m.version + " " : "") + "names no backend paths (a newer one does): only the renderer's calls reach " + backend);
		paths = [];
	} else if (!Array.isArray(m.backendPaths) || !m.backendPaths.every((p) => typeof p === "string")) {
		fail(marker + ": backendPaths isn't a list of paths");
	} else {
		paths = m.backendPaths;
		if (!paths.length) {
			console.error("homeport: no backend paths: only the renderer's calls reach " + backend);
		}
	}
}

const hostCall = process.env.RSC_HOST_CALL_PATH || "/__rsc/host-call";
for (const p of paths) {
	if (hostCall === p || hostCall.startsWith(p + "/")) {
		fail("backend path " + p + " holds the host-call endpoint (" + hostCall + "), which only the renderer calls: the host would send browsers to it");
	}
}
writeFileSync(out, paths.map((p) => p + "\n").join(""));
