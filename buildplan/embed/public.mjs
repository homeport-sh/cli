// public.mjs: the prerendered pages and assets a framework's own production
// server answers from files, before any of the app's code runs (no hook, no
// middleware), copied into .homeport/public so homeport's host answers them
// without waking the app:
//
//   immutable/<url>   a fingerprinted asset: cached a year
//   files/<url>       any other file, at its exact URL (never one ending in /)
//   pages/<url>.html  an HTML page: /about is pages/about.html, /about/ is
//                     pages/about/.html, / is pages/.html
//
// Only what the framework's server would answer itself, byte for byte: a URL
// it redirects (the other trailing-slash form), answers with headers of the
// app's, or runs any of the app's code for, is left out - the app answers it.
//
//   bin public.mjs sveltekit <adapter out> <marker>   (cwd: the bundle)
//   bin public.mjs astro <outDir> <marker>
//
// <marker> is the app's .output/edge.json (the edge contract, v2), written
// from the same tree for the edge to keep its pages: "static" lists the
// first MAX_STATIC of them, "files": true says they're files the host
// answers - the edge asks for each with a plain GET, never the app. The
// host answers every page in the tree, listed or not. A marker already
// there is the framework's own, and kept.
//
// It never fails the build: anything it can't read, it says, and leaves
// nothing behind. Run by the bundle's own bin (Node or Bun): node: only.
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const OUT = ".homeport/public";
// the edge contract's limits on "static": how many, and each path's bytes
const MAX_STATIC = 200, MAX_PATH = 200;
const say = (s) => console.log("homeport: " + s);

// URLs it serves: plain characters only (no escapes, nothing a server could
// decode two ways), absolute, no . or .. segments - no dotfiles, but under
// wellKnown (base/.well-known/), when its server answers those
const safe = /^\/([A-Za-z0-9._~!$&'()*+,;=:@-]+\/?)*$/;
function plainURL(u, wellKnown) {
	if (!safe.test(u)) return false;
	const rest = wellKnown && u.startsWith(wellKnown) ? u.slice(wellKnown.length - 1) : u;
	return rest.split("/").slice(1).every((s, i, a) => s === "" ? i === a.length - 1 : !s.startsWith("."));
}
const ext = (f) => {
	const b = f.slice(f.lastIndexOf("/") + 1);
	const i = b.lastIndexOf(".");
	return i > 0 ? b.slice(i).toLowerCase() : "";
};

class Tree {
	constructor(wellKnown) {
		this.wellKnown = wellKnown;
		this.urls = new Map(); // url -> [tree, src]
		this.left = []; // [url, why]
	}
	// place: a URL and the file its server answers it with
	put(url, src, immutable) {
		if (this.urls.has(url)) return;
		if (!plainURL(url, this.wellKnown)) return this.left.push([url, "its URL has characters only the app reads"]);
		const page = url.endsWith("/") || ext(src) === ".html" && !/\.html?$/.test(url);
		if (page && ext(src) !== ".html") return this.left.push([url, "not an HTML page"]);
		if (!page && !ext(url)) return this.left.push([url, "no file extension to give it a type"]);
		this.urls.set(url, [immutable && !page ? "immutable" : page ? "pages" : "files", src]);
	}
	write() {
		fs.rmSync(OUT, { recursive: true, force: true });
		const n = { immutable: 0, files: 0, pages: 0 };
		for (const [url, [tree, src]] of this.urls) {
			const dst = path.join(OUT, tree, tree === "pages" ? url + ".html" : url);
			fs.mkdirSync(path.dirname(dst), { recursive: true });
			fs.copyFileSync(src, dst);
			n[tree]++;
		}
		return n;
	}
}

// walk: every file under dir, as /-separated paths relative to it
function walk(dir, rel = "") {
	let out = [];
	for (const e of fs.readdirSync(path.join(dir, rel), { withFileTypes: true })) {
		const r = rel ? rel + "/" + e.name : e.name;
		if (e.isDirectory()) out = out.concat(walk(dir, r));
		else if (e.isFile()) out.push(r);
	}
	return out;
}
const isFile = (f) => {
	try {
		return fs.statSync(f).isFile();
	} catch {
		return false;
	}
};
const isDir = (f) => {
	try {
		return fs.statSync(f).isDirectory();
	} catch {
		return false;
	}
};

// SvelteKit's adapter-node: its server answers its asset table - client
// files, then prerendered pages - before SvelteKit runs (hooks included)
function sveltekit(out) {
	const table = path.join(out, "adapter-node.js");
	if (isFile(table)) {
		// adapter-node 6+: export const x = <JSON> | JSON.parse("<JSON>");
		const c = {};
		for (const line of fs.readFileSync(table, "utf8").split("\n")) {
			const m = /^export const (\w+) = (.*);$/.exec(line);
			if (!m) continue;
			try {
				const p = /^JSON\.parse\((".*")\)$/.exec(m[2]);
				c[m[1]] = p ? JSON.parse(JSON.parse(p[1])) : JSON.parse(m[2]);
			} catch {}
		}
		if (typeof c.base !== "string" || typeof c.app_path !== "string" || !c.assets?.entries || !c.prerendered_assets?.entries || !c.mime_types) {
			throw new Error(table + " isn't the asset table this adapter-node writes");
		}
		// its table is what it answers: .well-known too
		const t = new Tree(c.base + "/.well-known/");
		const immutable = "/" + c.app_path + "/immutable/";
		const typed = new Map();
		const add = (dir, tbl, client) => {
			const m = new Map(tbl.entries.map(([k, e]) => [k, e.file]));
			for (const [a, k] of tbl.aliases || []) if (m.has(k)) m.set(a, m.get(k));
			for (const [u, f] of m) {
				if (typed.has(u)) continue; // client assets win
				typed.set(u, true);
				// no type from the adapter: it'd send none
				if (!c.mime_types[ext(f)]) {
					t.left.push([u, "no type the adapter knows"]);
					continue;
				}
				t.put(u, path.join(dir, f), client && u.startsWith(immutable));
			}
		};
		add(path.join(out, "client" + c.base), c.assets, true);
		add(path.join(out, "prerendered" + c.base), c.prerendered_assets, false);
		return t;
	}
	// adapter-node 5: sirv on client/, then prerendered/ for the paths in its
	// prerendered set, then SvelteKit
	let set, appPath;
	const sources = [path.join(out, "handler.js"), path.join(out, "server/manifest.js")];
	for (const d of [path.join(out, "server/chunks")]) {
		if (isDir(d)) for (const f of fs.readdirSync(d)) if (f.endsWith(".js")) sources.push(path.join(d, f));
	}
	for (const f of sources) {
		if (!isFile(f)) continue;
		const s = fs.readFileSync(f, "utf8");
		const m = /const prerendered = new Set\((\[[^\]]*\])\)/.exec(s);
		if (m && !set) set = JSON.parse(m[1]);
		const a = /\bappPath: ("[^"]*")/.exec(s);
		if (a && appPath === undefined) appPath = JSON.parse(a[1]);
	}
	if (!set || appPath === undefined || !isFile(path.join(out, "handler.js"))) {
		throw new Error("no adapter-node output in " + out);
	}
	const t = new Tree(); // sirv: no dotfiles
	const client = path.join(out, "client");
	const files = isDir(client) ? walk(client) : [];
	const has = new Set(files.map((f) => "/" + f));
	const immutable = "/" + appPath + "/immutable/";
	for (const f of files) {
		if (/\.(br|gz)$/.test(f)) continue; // its precompressed copies
		t.put("/" + f, path.join(client, f), ("/" + f).startsWith(immutable));
	}
	const pre = path.join(out, "prerendered");
	for (const u of set) {
		// sirv on client/ answers first, with its own fallbacks
		const bare = u.replace(/\/$/, "");
		if ([u, bare + ".html", bare + ".htm", bare + "/index.html"].some((c) => has.has(c))) continue;
		const tries = u.endsWith("/") ? [u + "index.html"] : [u, u + ".html", u + "/index.html"];
		const f = tries.find((c) => isFile(path.join(pre, c)));
		if (f) t.put(u, path.join(pre, f), false);
	}
	return t;
}

// Astro's node adapter, standalone: its static handler answers files in the
// client folder before the app (middleware included) - each URL as it maps
// them, by the build's trailingSlash and build.format
function astro(outDir) {
	const server = path.join(outDir, "server");
	let man;
	for (const f of [path.join(server, "entry.mjs")].concat(isDir(path.join(server, "chunks")) ? fs.readdirSync(path.join(server, "chunks")).map((c) => path.join(server, "chunks", c)) : [])) {
		if (!isFile(f) || !f.endsWith(".mjs")) continue;
		const s = fs.readFileSync(f, "utf8");
		for (let at = s.indexOf("deserializeManifest("); at >= 0 && !man; at = s.indexOf("deserializeManifest(", at + 1)) {
			const j = jsonAt(s, at + "deserializeManifest(".length);
			if (j && j.adapterName) man = j;
		}
		if (man) break;
	}
	if (!man) throw new Error("no Astro manifest in " + server);
	if (man.adapterName !== "@astrojs/node") throw new Error("the adapter is " + man.adapterName + ", not @astrojs/node");
	const mode = man.trailingSlash, format = man.buildFormat;
	if (!["ignore", "never", "always"].includes(mode) || typeof man.base !== "string" || typeof man.assetsDir !== "string") {
		throw new Error("an Astro manifest this doesn't know");
	}
	const client = path.join(server, path.relative(fileURLToPath(man.buildServerDir), fileURLToPath(man.buildClientDir)));
	if (!isDir(client)) throw new Error("no client folder at " + client);
	// headers the adapter adds to prerendered pages (staticHeaders): the app's
	const headers = isFile(path.join(outDir, "_headers.json"));
	const base = man.base.replace(/\/+$/, "");
	// send denies dotfiles, but /.well-known/ (after the base)
	const t = new Tree(base + "/.well-known/");
	const immutable = "/" + man.assetsDir + "/";
	const files = walk(client);
	const put = (u, f) => {
		if (headers && !u.startsWith(base + immutable)) return t.left.push([u, "the adapter adds its static headers to it"]);
		t.put(u, path.join(client, f), u.startsWith(base + immutable));
	};
	// exact files first: send answers any file at its path ("always" too:
	// it has an extension)
	for (const f of files) {
		if (mode === "always" && !ext(f)) continue; // redirected to the slash form
		put(base + "/" + f, f);
	}
	for (const f of files) {
		if (f === "index.html" || f.endsWith("/index.html")) {
			// a folder: its index.html, by trailingSlash
			const d = f.slice(0, -"index.html".length); // "" or "x/"
			if (d === "") {
				// the root: base "/" answers at /, any base at base/ (not base
				// alone, nor - with never - base/, which redirects)
				if (base === "" || mode !== "never") put(base + "/", f);
				continue;
			}
			if (mode !== "never") put(base + "/" + d, f);
			if (mode !== "always") put(base + "/" + d.slice(0, -1), f);
		} else if (f.endsWith(".html") && (format === "file" || format === "preserve") && mode !== "always") {
			// clean URLs: send tries .html - unless a folder of the name
			// takes it
			const u = f.slice(0, -".html".length);
			if (!isDir(path.join(client, u))) put(base + "/" + u, f);
		}
	}
	return t;
}

// jsonAt: the JSON value starting at i in s
function jsonAt(s, i) {
	if (s[i] !== "{") return null;
	let depth = 0, str = false;
	for (let k = i; k < s.length; k++) {
		const ch = s[k];
		if (str) {
			if (ch === "\\") k++;
			else if (ch === '"') str = false;
		} else if (ch === '"') str = true;
		else if (ch === "{" || ch === "[") depth++;
		else if (ch === "}" || ch === "]") {
			if (--depth === 0) {
				try {
					return JSON.parse(s.slice(i, k + 1));
				} catch {
					return null;
				}
			}
		}
	}
	return null;
}

// edgeMarker writes marker from the tree's pages, unless one is there: the
// first MAX_STATIC of them, shortest first, as "static"; the build, a hash
// of what they are. What it wrote, or null.
function edgeMarker(t, marker) {
	if (!marker) return null;
	if (fs.existsSync(marker)) {
		say("edge.json: the build wrote its own (" + marker + "), kept as it is");
		return null;
	}
	const pages = [...t.urls].filter(([u, [tr]]) => tr === "pages" && Buffer.byteLength(u) <= MAX_PATH).map(([u]) => u);
	pages.sort((a, b) => a.length - b.length || (a < b ? -1 : a > b ? 1 : 0));
	if (!pages.length) return null;
	const h = crypto.createHash("sha256");
	for (const u of pages) h.update(u + "\0" + crypto.createHash("sha256").update(fs.readFileSync(t.urls.get(u)[1])).digest("hex") + "\n");
	const m = { v: 2, build: h.digest("hex").slice(0, 16), shells: [], static: pages.slice(0, MAX_STATIC), files: true };
	fs.mkdirSync(path.dirname(marker), { recursive: true });
	fs.writeFileSync(marker, JSON.stringify(m) + "\n");
	return { listed: m.static.length, of: pages.length };
}

const [kind, dir, marker] = process.argv.slice(2);
let wrote = false;
try {
	const t = kind === "sveltekit" ? sveltekit(dir) : kind === "astro" ? astro(dir) : null;
	if (!t) throw new Error("no framework " + kind);
	const n = t.write();
	const e = edgeMarker(t, marker);
	wrote = !!e;
	say(`served without the app, before any of its code runs (as ${kind === "astro" ? "Astro's node adapter" : "adapter-node"} serves them): ${n.pages} prerendered page(s), ${n.files} file(s), ${n.immutable} immutable asset(s)`);
	const pages = [...t.urls].filter(([, [tr]]) => tr === "pages").map(([u]) => u);
	if (pages.length) say("  pages: " + pages.slice(0, 20).join(" ") + (pages.length > 20 ? ` (and ${pages.length - 20} more)` : ""));
	for (const [u, why] of t.left.slice(0, 20)) say(`  left to the app: ${u} - ${why}`);
	if (t.left.length > 20) say(`  (and ${t.left.length - 20} more left to the app)`);
	say("  anything else - server-rendered pages, other methods, the other trailing-slash form - is the app's, and wakes it");
	if (e) {
		say(`edge.json: ${e.listed} of the ${e.of} page(s) kept at the edge as well` + (e.of > e.listed ? ` (its most is ${MAX_STATIC}: the rest from the host alone, still without the app)` : ""));
	}
} catch (e) {
	fs.rmSync(OUT, { recursive: true, force: true });
	if (wrote) fs.rmSync(marker, { force: true });
	say("prerendered pages are served by the app, which wakes for them: " + (e && e.message ? e.message : e));
}
