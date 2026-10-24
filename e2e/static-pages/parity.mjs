// parity.mjs: what the build put in .homeport/public is exactly what the
// app's own server answers from files, with none of the app's code run.
//
//   node parity.mjs <bundle> <app url> <marker header> [--hook <path>...] [--left <path>...]
//
// Every URL in the tree: the running app answers it 200, with the same
// bytes, and without the marker header the app's hook (or middleware) sets
// on every response it runs for. Each --hook path: the app's code runs for
// it (the marker is there), and it is NOT in the tree. Each --left path (a
// redirect, another base): NOT in the tree.
import fs from "node:fs";
import path from "node:path";

const [bundle, app, marker, ...rest] = process.argv.slice(2);
const hook = [], left = [];
for (let i = 0, to = null; i < rest.length; i++) {
	if (rest[i] === "--hook") to = hook;
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

const urls = new Map();
for (const tree of ["immutable", "files", "pages"]) {
	for (const f of walk(path.join(root, tree))) {
		urls.set(tree === "pages" ? f.slice(0, -".html".length) : f, path.join(root, tree, f));
	}
}
if (urls.size === 0) bad("nothing in " + root);

for (const [u, f] of urls) {
	const r = await fetch(app + u, { redirect: "manual" });
	const body = Buffer.from(await r.arrayBuffer());
	if (r.status !== 200) bad(`${u}: the app answers ${r.status}`);
	else if (r.headers.has(marker)) bad(`${u}: the app ran its code for it (${marker})`);
	else if (!body.equals(fs.readFileSync(f))) bad(`${u}: not the bytes the app answers`);
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
process.exit(fail);
