// parity.mjs: what the build put in .homeport/public is exactly what the
// app's own server answers from files, with none of the app's code run.
//
//   node parity.mjs <bundle> <app url> <marker header> [--edge <edge.json>] [--hook <path>...] [--left <path>...]
//
// Every URL in the tree: the running app answers it 200, with the same
// bytes, and without the marker header the app's hook (or middleware) sets
// on every response it runs for - with exactly the headers routes.json
// gives it besides the file's own, and an immutable asset, the
// Cache-Control the host sends for it. Each of routes.json's redirects, at
// paths its pattern matches: the app answers what the host would (status,
// Location, a 308's Refresh), the host's first match in order. Each --hook path: the app's code runs for
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

let routes = { headers: [], redirects: [] };
try {
	routes = JSON.parse(fs.readFileSync(path.join(root, "routes.json"), "utf8"));
} catch {}
const sets = new Map();
for (const h of routes.headers || []) for (const u of h.urls) sets.set(u, h.set);
// what a file server sends of its own: the rest of an answer's headers are
// the ones routes.json gives the file
const own = new Set(["content-type", "content-length", "content-encoding", "etag", "last-modified", "accept-ranges", "vary", "date", "connection", "keep-alive", "transfer-encoding", "cache-control"]);
const headerList = (pairs) => pairs.map(([k, v]) => k.toLowerCase() + ": " + v).sort().join("\n");

// answer: what the host answers path with by routes.json's redirects, as the
// engine matches them (homeport_public): the first whose pattern matches,
// its params put in its destination; null none
function answer(path_) {
	const trailing = path_.length > 1 && path_.endsWith("/");
	const segs = path_ === "/" ? [] : path_.slice(1, trailing ? -1 : undefined).split("/");
	for (const r of routes.redirects || []) {
		const pt = r.from.length > 1 && r.from.endsWith("/");
		if (pt && !trailing) continue;
		const ps = r.from === "/" ? [] : r.from.slice(1, pt ? -1 : undefined).split("/");
		const params = {};
		let ok = true, i = 0;
		for (const [j, p] of ps.entries()) {
			const m = /^:(\w+)([*+]?)$/.exec(p);
			if (m && m[2]) {
				const rest = segs.slice(i);
				if (m[2] === "+" && !rest.length) ok = false;
				params[m[1]] = rest.join("/");
				i = segs.length;
			} else if (m) {
				if (i >= segs.length) ok = false;
				params[m[1]] = segs[i++];
			} else if (i >= segs.length || segs[i++].toLowerCase() !== p.toLowerCase()) ok = false;
			if (!ok || (m && m[2] && j !== ps.length - 1)) break;
		}
		if (!ok || i !== segs.length) continue;
		const to = r.to.replace(/\/:(\w+)\*/g, (_, n) => (params[n] ? "/" + params[n] : "")).replace(/:(\w+)[*+]?/g, (_, n) => params[n]);
		return { status: r.status, location: to };
	}
	return null;
}
// samples: paths a pattern matches - each param a value, a repeat's none,
// one and two
function samples(from) {
	let out = [""];
	const t = from.length > 1 && from.endsWith("/");
	for (const p of (from === "/" ? [] : from.slice(1, t ? -1 : undefined).split("/"))) {
		const m = /^:(\w+)([*+]?)$/.exec(p);
		const vals = !m ? [p] : m[2] === "*" ? ["", "a", "a/b"] : m[2] === "+" ? ["a", "a/b"] : ["S1"];
		out = out.flatMap((o) => vals.map((v) => (v === "" ? o : o + "/" + v)));
	}
	return out.map((o) => (o || "/") + (t ? "/" : ""));
}

const directives = (cc) => (cc ?? "").toLowerCase().split(",").map((d) => d.trim()).sort().join(",");
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
	else if (headerList([...r.headers].filter(([k]) => !own.has(k))) !== headerList((sets.get(u) || []).filter(([k]) => k.toLowerCase() !== "cache-control"))) {
		bad(`${u}: the app's headers aren't routes.json's:\n${headerList([...r.headers].filter(([k]) => !own.has(k)))}\n  routes.json:\n${headerList(sets.get(u) || [])}`);
	}
	// routes.json's, else the host's for an immutable asset: the app's own
	// directives (sirv writes them without spaces, Next.js and express with)
	else if ((sets.get(u) || []).some(([k]) => k.toLowerCase() === "cache-control")) {
		const cc = sets.get(u).find(([k]) => k.toLowerCase() === "cache-control")[1];
		if (r.headers.get("cache-control") !== cc) bad(`${u}: the app answers Cache-Control ${r.headers.get("cache-control")}, not routes.json's ${cc}`);
		else ok(`${u}: the app's own answer, from a file, none of its code run, its config's headers (${sets.get(u).length})`);
	}
	else if (immutable.has(u) && directives(r.headers.get("cache-control")) !== "immutable,max-age=31536000,public") {
		bad(`${u}: the app answers Cache-Control ${r.headers.get("cache-control")}, not the host's year, immutable`);
	}
	else ok(`${u}: the app's own answer, from a file, none of its code run` + (sets.has(u) ? `, its config's headers (${sets.get(u).length})` : ""));
}
for (const red of routes.redirects || []) {
	for (const p of samples(red.from)) {
		const want = answer(p);
		const r = await fetch(app + p, { redirect: "manual" });
		const got = { status: r.status, location: r.headers.get("location"), refresh: r.headers.get("refresh") };
		if (!want) bad(`${p}: a sample of ${red.from} the host doesn't redirect`);
		else if (got.status !== want.status || got.location !== want.location || got.refresh !== (want.status === 308 ? "0;url=" + want.location : null) || r.headers.has(marker)) {
			bad(`${p}: the app answers ${JSON.stringify(got)}, the host would ${JSON.stringify(want)}`);
		} else ok(`${p}: ${want.status} ${want.location}, as the host answers it without the app`);
	}
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
