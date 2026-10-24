// parity.mjs: what the build put in .homeport/public is exactly what the
// app's own server answers from files, with none of the app's code run.
//
//   node parity.mjs <bundle> <app url> <marker header> [--edge <edge.json>] [--hook <path>...] [--left <path>...]
//
// Every URL in the tree: the running app answers it 200, with the same
// bytes, and without the marker header the app's hook (or middleware) sets
// on every response it runs for - and an immutable asset, the Cache-Control
// the host sends for it. Each --hook path: the app's code runs for
// it (the marker is there), and it is NOT in the tree. Each --left path (a
// redirect, another base): NOT in the tree. --edge: the edge marker the
// build wrote lists only pages of the tree (so each is checked above),
// "files": true, and no --hook or --left path.
import fs from "node:fs";
import path from "node:path";

const [bundle, app, marker, ...rest] = process.argv.slice(2);
const hook = [], left = [];
let edge = null;
for (let i = 0, to = null; i < rest.length; i++) {
	if (rest[i] === "--edge") edge = rest[++i];
	else if (rest[i] === "--hook") to = hook;
	else if (rest[i] === "--left") to = left;
	else if (to) to.push(rest[i]);
	else throw new Error("a path before --hook or --left: " + rest[i]);
}
const root = path.join(bundle, ".homeport/public");
let fail = 0;
const ok = (what) => console.log("  ok    " + what);
const bad = (what) => {
	console.log("  FAIL  " + what);
	fail = 1;
};

function walk(dir, rel = "") {
	let out = [];
	if (!fs.existsSync(dir)) return out;
	for (const e of fs.readdirSync(path.join(dir, rel), { withFileTypes: true })) {
		const r = rel + "/" + e.name;
		out = e.isDirectory() ? out.concat(walk(dir, r)) : out.concat([r]);
	}
	return out;
}

const urls = new Map(), immutable = new Set();
for (const tree of ["immutable", "files", "pages"]) {
	for (const f of walk(path.join(root, tree))) {
		urls.set(tree === "pages" ? f.slice(0, -".html".length) : f, path.join(root, tree, f));
		if (tree === "immutable") immutable.add(f);
	}
}
if (urls.size === 0) bad("nothing in " + root);

for (const [u, f] of urls) {
	const r = await fetch(app + u, { redirect: "manual" });
	const body = Buffer.from(await r.arrayBuffer());
	if (r.status !== 200) bad(`${u}: the app answers ${r.status}`);
	else if (r.headers.has(marker)) bad(`${u}: the app ran its code for it (${marker})`);
	else if (!body.equals(fs.readFileSync(f))) bad(`${u}: not the bytes the app answers`);
	// the host's for an immutable asset: the app's own, exactly
	else if (immutable.has(u) && r.headers.get("cache-control") !== "public, max-age=31536000, immutable") {
		bad(`${u}: the app answers Cache-Control ${r.headers.get("cache-control")}, not the host's year, immutable`);
	}
	else ok(`${u}: the app's own answer, from a file, none of its code run`);
}
for (const u of left) {
	if (urls.has(u)) bad(`${u}: served without the app, but the app doesn't answer it from a file`);
	else ok(`${u}: left to the app`);
}
for (const u of hook) {
	const r = await fetch(app + u, { redirect: "manual" });
	if (urls.has(u)) bad(`${u}: the app's code runs for it, but it's served without the app`);
	else if (!r.headers.has(marker)) bad(`${u}: no ${marker} - the check proves nothing`);
	else ok(`${u}: the app's code runs for it (${r.status}), and it's left to the app`);
}
if (edge) {
	let m = null;
	try {
		m = JSON.parse(fs.readFileSync(edge, "utf8"));
	} catch (e) {
		bad(`${edge}: no edge marker (${e.message})`);
	}
	if (m) {
		if (m.v !== 2 || m.files !== true || !Array.isArray(m.static) || !m.static.length) bad(`${edge}: not the files marker: ${JSON.stringify(m)}`);
		for (const u of m.static || []) {
			if (!urls.has(u) || !fs.existsSync(path.join(root, "pages", u + ".html"))) bad(`${u}: listed for the edge, but not a page in the tree`);
		}
		for (const u of hook.concat(left)) if ((m.static || []).includes(u)) bad(`${u}: listed for the edge, but left to the app`);
		ok(`${edge}: ${(m.static || []).length} page(s) for the edge, each a page of the tree`);
	}
}
process.exit(fail);
