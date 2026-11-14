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
// routes.json, beside the trees, is what else the server does before any of
// the app's code, as data the host applies (docs/edge-caching.md, "The
// public tree's routes"): headers it adds to some of the tree's files (a
// file's set, by its URL), and redirects it answers for any path: Next.js's
// config's headers() and redirects(), and a files manifest's headers.
//
//   bin public.mjs files <manifest> [marker]           (cwd: where the tree goes)
//   bin public.mjs sveltekit <adapter out> <marker>   (cwd: the bundle)
//   bin public.mjs astro <outDir> <marker>
//   bin public.mjs next <standalone app folder> <marker>
//   bin public.mjs react-router <server build> <marker>
//   bin public.mjs remix <server build> <marker>
//
// files: homeport's files manifest (docs/edge-caching.md, "The files
// manifest"), which a framework writes to say itself what its server answers
// from files - rsc-kit's .output/files.json. Its own build's server is a
// binary: the tree goes in the app's folder, for the builder to ship.
//
// Next.js, react-router-serve and remix-serve: their content-hashed assets
// alone (immutable/), which their servers answer from files with the same
// year-long immutable Cache-Control.
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
// routes.json's limits, as the host's: distinct header sets, headers in a
// set, a value's bytes; redirects, a redirect's from and to
const MAX_SETS = 64, MAX_SET = 32, MAX_VALUE = 8192, MAX_REDIRECTS = 128, MAX_FROM = 512, MAX_TO = 2048, MAX_ROUTES = 1 << 20;
// a header the host sends from routes.json, as it checks one: a name of its
// headers: rules' (docs/headers.md), never one homeport or the response
// itself sets; a value of printable ASCII, no ` { }, no spaces around it
const REFUSED = new Set("set-cookie content-length transfer-encoding connection keep-alive proxy-connection te trailer upgrade content-encoding vary content-range accept-ranges etag last-modified date alt-svc location x-origin-auth x-robots-tag defer match server via".split(" "));
function hostHeader(name, value) {
	if (!/^[A-Za-z][A-Za-z0-9-]{0,63}$/.test(name)) return "its name";
	const l = name.toLowerCase();
	if (REFUSED.has(l) || l.startsWith("x-homeport-")) return "homeport's or the response's own";
	if (typeof value !== "string" || !/^[ -~]{1,8192}$/.test(value) || /^ | $|[`{}]/.test(value) || Buffer.byteLength(value) > MAX_VALUE) return "its value";
	return null;
}
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
		this.urls = new Map(); // url -> [tree, src, copies]
		this.left = []; // [url, why]
		this.headers = new Map(); // url -> its set's key
		this.sets = new Map(); // key (JSON) -> {set: [[name, value]...], exact?}
		this.redirects = []; // {from, to, status, caseless?, refresh?}
	}
	// header: the headers its server adds to url's file; exact, all it
	// sends with it (the host adds no Cache-Control of its own); false when
	// the host can't send them (the file's then the app's)
	header(url, set, exact = false) {
		if (!set.length && !exact) return true;
		// a URL the host gives headers to: letters, digits and -._~ alone
		if (set.length > MAX_SET || !/^\/([A-Za-z0-9._~-]+\/)*[A-Za-z0-9._~-]*$/.test(url)) return false;
		const key = JSON.stringify([set, exact]);
		if (!this.sets.has(key)) {
			if (this.sets.size === MAX_SETS) return false;
			this.sets.set(key, exact ? { set, exact } : { set });
		}
		this.headers.set(url, key);
		return true;
	}
	// place: a URL and the file its server answers it with; copies, the
	// extensions of the compressed copies beside it its server sends too
	// (.br, .gz: the host's), else the builder writes its own
	put(url, src, immutable, copies = []) {
		if (this.urls.has(url)) return;
		if (!plainURL(url, this.wellKnown)) return this.left.push([url, "its URL has characters only the app reads"]);
		// the host serves a file's .br and .gz (the builder's) for the file's
		// URL: a URL naming one is the app's
		if (/\.(br|gz)$/i.test(url)) return this.left.push([url, "a compressed copy (the host serves its own, for the file's URL)"]);
		const page = url.endsWith("/") || ext(src) === ".html" && !/\.html?$/.test(url);
		if (page && ext(src) !== ".html") return this.left.push([url, "not an HTML page"]);
		if (!page && !ext(url)) return this.left.push([url, "no file extension to give it a type"]);
		this.urls.set(url, [immutable && !page ? "immutable" : page ? "pages" : "files", src, copies]);
	}
	write() {
		fs.rmSync(OUT, { recursive: true, force: true });
		const n = { immutable: 0, files: 0, pages: 0 };
		let routes = this.routes();
		if (Buffer.byteLength(routes) > MAX_ROUTES) {
			// more than the host reads: the files with headers are the app's
			for (const url of this.headers.keys()) {
				if (this.urls.delete(url)) this.left.push([url, "more headers, or header sets, than homeport sends from files"]);
			}
			this.headers.clear();
			routes = this.routes();
		}
		for (const [url, [tree, src, copies]] of this.urls) {
			const dst = path.join(OUT, tree, tree === "pages" ? url + ".html" : url);
			fs.mkdirSync(path.dirname(dst), { recursive: true });
			fs.copyFileSync(src, dst);
			for (const c of copies) fs.copyFileSync(src + c, dst + c);
			n[tree]++;
		}
		if (routes) {
			fs.mkdirSync(OUT, { recursive: true });
			fs.writeFileSync(path.join(OUT, "routes.json"), routes);
		}
		n.headers = [...this.headers.keys()].filter((u) => this.urls.has(u)).length;
		n.redirects = this.redirects.length;
		return n;
	}
	// routes: routes.json's text ("" none) - each set with its files' URLs
	routes() {
		const sets = new Map([...this.sets.keys()].map((k) => [k, []]));
		for (const [url, key] of this.headers) if (this.urls.has(url)) sets.get(key).push(url);
		const headers = [...sets].filter(([, urls]) => urls.length).map(([k, urls]) => ({ set: this.sets.get(k).set, urls, ...(this.sets.get(k).exact && { exact: true }) }));
		return headers.length || this.redirects.length ? JSON.stringify({ v: 1, headers, redirects: this.redirects }) + "\n" : "";
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

// files: homeport's files manifest (docs/edge-caching.md, "The files
// manifest") - {"v":1,"root","files","precompressed","headers","middleware"}
// - read whole or not at all: another version, middleware, a root outside
// the manifest's folder is the app's, every file. Its files alone, each at
// its URL, a file with its headers (every rule whose match is the URL, or a
// prefix ending /**, in order, a later value replacing an earlier one's);
// one with the year-long immutable Cache-Control is a fingerprinted asset
// (immutable/), any other a file (files/). A file the host would send a
// header the app doesn't (none of its own: the host's Cache-Control), or
// one it can't send, is the app's.
const IMMUTABLE = "public, max-age=31536000, immutable";
function files(manifest) {
	let m;
	try {
		m = JSON.parse(fs.readFileSync(manifest, "utf8"));
	} catch (e) {
		throw new Error(e && e.code === "ENOENT" ? "no " + manifest : manifest + " isn't JSON");
	}
	if (!m || typeof m !== "object" || m.v !== 1) throw new Error(manifest + ": not a files manifest this reads (v 1)");
	if (m.middleware !== false) throw new Error(manifest + ": the app's code runs for its files (middleware)");
	if (typeof m.root !== "string" || !Array.isArray(m.files) || (m.headers !== undefined && !Array.isArray(m.headers))) throw new Error(manifest + ": a files manifest this doesn't know");
	const at = path.dirname(manifest);
	const root = path.resolve(at, m.root);
	if (path.isAbsolute(m.root) || (root !== path.resolve(at) && !root.startsWith(path.resolve(at) + path.sep)) || !isDir(root)) throw new Error(manifest + ": its root isn't a folder beside it");
	const realRoot = fs.realpathSync(root);
	const enc = { br: ".br", gzip: ".gz" }; // the host's copies (not zstd)
	const copyExts = (Array.isArray(m.precompressed) ? m.precompressed : []).map((e) => enc[e]).filter(Boolean);
	const rules = [];
	for (const r of m.headers || []) {
		if (!r || typeof r.match !== "string" || !r.match.startsWith("/") || !r.headers || typeof r.headers !== "object") throw new Error(manifest + ": a header rule this doesn't know");
		const set = Object.entries(r.headers);
		if (set.some(([, v]) => typeof v !== "string")) throw new Error(manifest + ": a header rule this doesn't know");
		// exact, or a prefix ending /**; any other match is no rule here
		if (r.match.endsWith("/**")) rules.push({ prefix: r.match.slice(0, -2), set });
		else if (!r.match.includes("*")) rules.push({ exact: r.match, set });
	}
	const t = new Tree();
	for (const u of m.files) {
		if (typeof u !== "string" || !u.startsWith("/")) continue;
		const src = path.join(root, u);
		let real;
		try {
			real = fs.realpathSync(src);
		} catch {
			t.left.push([u, "listed, but not in " + m.root]);
			continue;
		}
		if (!real.startsWith(realRoot + path.sep) || !isFile(real)) {
			t.left.push([u, "not a file in " + m.root]);
			continue;
		}
		// as its server sends them: by name in any case, the last one set
		const hs = new Map();
		for (const r of rules) {
			if (r.exact !== undefined ? r.exact !== u : !u.startsWith(r.prefix)) continue;
			for (const [k, v] of r.set) hs.set(k.toLowerCase(), [k, v]);
		}
		const set = [...hs.values()];
		const bad = set.map(([k, v]) => [k, hostHeader(k, v)]).find(([, why]) => why);
		if (bad) {
			t.left.push([u, `a header of the app's homeport can't send (${bad[0]}: ${bad[1]})`]);
			continue;
		}
		// none: the file goes out with exactly its set, the host adding no
		// Cache-Control of its own (an exact set)
		const cc = hs.get("cache-control");
		const copies = copyExts.filter((c) => {
			try {
				return fs.lstatSync(src + c).isFile();
			} catch {
				return false;
			}
		});
		t.put(u, src, !!cc && cc[1].toLowerCase() === IMMUTABLE, copies);
		if (t.urls.has(u) && !t.header(u, set, !cc)) {
			t.urls.delete(u);
			t.left.push([u, "more headers, or header sets, than homeport sends from files"]);
		}
	}
	return t;
}

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

// Next.js standalone: the router answers /_next/static/<file> from
// .next/static, Cache-Control a year and immutable unless the config's
// headers set one (the service worker's excepted) - after the config's
// headers and redirects, its middleware (or proxy) and beforeFiles
// rewrites. The config's headers go with the files they match, in
// routes.json; a URL middleware, a rewrite, a redirect, onMatchHeaders or
// a header rule with has or missing could take is left to the app. Its
// redirects up to the first the host can't answer as Next does go in
// routes.json too. assetPrefix and i18n: the app's, all of it.
function next(dir) {
	const nd = path.join(dir, ".next");
	const read = (f) => JSON.parse(fs.readFileSync(path.join(nd, f), "utf8"));
	let cfg, routes;
	try {
		cfg = read("required-server-files.json").config;
		routes = read("routes-manifest.json");
	} catch {
		throw new Error("no Next.js standalone manifests in " + nd);
	}
	if (!cfg || typeof cfg !== "object") throw new Error("a Next.js config this doesn't know");
	if (cfg.assetPrefix) throw new Error("assetPrefix is set (" + cfg.assetPrefix + "): its assets are the app's to place");
	if (cfg.i18n) throw new Error("i18n is set: its routing is the app's");
	const base = typeof cfg.basePath === "string" ? cfg.basePath.replace(/\/+$/, "") : "";
	// path-to-regexp's: case-insensitive unless the config says
	const flags = routes.caseSensitive === true ? "" : "i";
	const list = (l) => (Array.isArray(l) ? l : []).filter((r) => r && typeof r.regex === "string");
	// what runs before the files and may take one: each a regex (has/missing:
	// may match)
	const rw = routes.rewrites && !Array.isArray(routes.rewrites) ? routes.rewrites.beforeFiles : [];
	const config = [...list(routes.onMatchHeaders), ...list(routes.redirects), ...list(rw)].map((r) => r.regex);
	let mw = [];
	const mm = (() => {
		try {
			return read("server/middleware-manifest.json");
		} catch {
			return null;
		}
	})();
	const fc = (() => {
		try {
			return read("server/functions-config-manifest.json");
		} catch {
			return null;
		}
	})();
	if (!mm || !fc) throw new Error("no middleware manifests in " + nd + "/server");
	const edge = mm.middleware && mm.middleware["/"];
	const node = fc.functions && fc.functions["/_middleware"];
	if (edge || node) mw = (edge ? edge.matchers : node.matchers) || [{ regexp: ".*" }];
	const re = (r) => {
		try {
			return [new RegExp(r), new RegExp(r, "i")];
		} catch {
			return [/^/];
		}
	};
	const configRe = config.map(re).flat(), mwRe = mw.map((m) => re(m.regexp)).flat();
	const headerRules = nextHeaderRules(Array.isArray(routes.headers) ? routes.headers : [], flags);
	const static_ = path.join(nd, "static");
	if (!isDir(static_)) throw new Error("no .next/static in " + dir);
	const t = new Tree();
	for (const f of walk(static_)) {
		const u = base + "/_next/static/" + f;
		if (f.startsWith("service-worker/")) {
			t.left.push([u, "the service worker: its own headers"]);
			continue;
		}
		if (mwRe.some((r) => r.test(u))) {
			t.left.push([u, "its middleware runs for it"]);
			continue;
		}
		if (configRe.some((r) => r.test(u))) {
			t.left.push([u, "the config's redirects or rewrites may take it"]);
			continue;
		}
		const h = nextHeaders(headerRules, u);
		if (typeof h === "string") {
			t.left.push([u, h]);
			continue;
		}
		t.put(u, path.join(static_, f), true);
		if (t.urls.has(u) && !t.header(u, h)) {
			t.urls.delete(u);
			t.left.push([u, "more headers, or header sets, than homeport sends from files"]);
		}
	}
	t.redirects = nextRedirects(Array.isArray(routes.redirects) ? routes.redirects : [], base, flags);
	return t;
}

// pathKeys: path-to-regexp's params in source, in order - {name, repeat},
// an unnamed group's name its index - or null for a source this can't read
function pathKeys(source) {
	if (source.includes("{")) return null;
	const keys = [];
	let unnamed = 0;
	const group = (i) => {
		for (let d = 0; i < source.length; i++) {
			if (source[i] === "\\") i++;
			else if (source[i] === "(") d++;
			else if (source[i] === ")" && --d === 0) return i + 1;
		}
		return -1;
	};
	for (let i = 0; i < source.length; i++) {
		let name = null, j = i;
		if (source[i] === "\\") {
			i++;
			continue;
		}
		if (source[i] === ":") {
			const m = /^\w+/.exec(source.slice(i + 1));
			if (!m) return null;
			name = m[0];
			j = i + 1 + name.length;
			if (source[j] === "(") j = group(j);
		} else if (source[i] === "(") {
			name = unnamed++;
			j = group(i);
		} else continue;
		if (j < 0) return null;
		keys.push({ name, repeat: source[j] === "*" || source[j] === "+" });
		i = j - 1;
	}
	return keys;
}

// nextHeaderRules: the config's headers as its router matches them
function nextHeaderRules(rules, flags) {
	return rules.map((r) => {
		let regex = null;
		try {
			regex = new RegExp(r.regex, flags);
		} catch {}
		return { r, regex, keys: typeof r.source === "string" ? pathKeys(r.source) : null };
	});
}

// nextHeaders: the headers Next's router adds to url's answer, as
// resolve-routes does - each matching rule's in order, a later one's value
// replacing an earlier's (setHeader, by name in any case), :params
// compiled - or why the host can't send them
function nextHeaders(rules, url) {
	const res = {};
	for (const { r, regex, keys } of rules) {
		if (!regex) return "a header rule this can't read";
		const m = regex.exec(url);
		if (!m) continue;
		if (r.has || r.missing) return "a header rule with has or missing matches it";
		// its named params (getPathMatch drops unnamed ones): those that matched
		const params = new Map();
		for (const [i, k] of (keys || []).entries()) if (typeof k.name === "string" && m[i + 1] !== undefined) params.set(k.name, k);
		for (const h of Array.isArray(r.headers) ? r.headers : []) {
			let { key, value } = h || {};
			if (typeof key !== "string" || typeof value !== "string") return "a header rule this can't read";
			if (params.size || !keys) {
				// compileNonPath: :name is the param's value; anything else of
				// path-to-regexp's in it, the app's
				const compiled = [key, value].map((s) => {
					if (!s.includes(":") && !s.includes("\\")) return s;
					if (!keys || s.includes("\\")) return null;
					let bad = false;
					const out = s.replace(/:(\w+)([*?+])?/g, (all, name, mod) => {
						const k = params.get(name);
						if (!k) return all;
						if (mod || k.repeat) bad = true;
						return m[keys.indexOf(k) + 1];
					});
					return bad ? null : out;
				});
				if (compiled.includes(null)) return "a header with a :param this doesn't compile";
				[key, value] = compiled;
			}
			if (key.toLowerCase() === "set-cookie") return "a header rule sets a cookie";
			res[key] = value;
		}
	}
	// as setHeader sends them: by name in any case, the last one set
	const out = new Map();
	for (const [k, v] of Object.entries(res)) out.set(k.toLowerCase(), [k, v]);
	const set = [...out.values()];
	for (const [k, v] of set) {
		const why = hostHeader(k, v);
		if (why) return `a header of the config's homeport can't send (${k}: ${why})`;
	}
	return set;
}

// nextRedirects: the config's redirects, in order, up to the first the host
// can't answer as Next's router does (one with has or missing, a :param
// with a pattern of its own, a destination with a query) - any after it
// stays with the app, whose router tries that one first
function nextRedirects(rules, base, flags) {
	const out = [];
	if (flags !== "i") return out; // caseSensitive: the host's match any case
	for (const r of rules) {
		const x = nextRedirect(r, base);
		if (!x || out.length === MAX_REDIRECTS) break;
		out.push(x);
	}
	return out;
}

// nextRedirect: one redirect as routes.json has it, or null. Its pattern is
// checked against the regex Next built from it - path-to-regexp's, and
// Next's: (?!<base>/_next) unless it's internal, an optional / at the end
// unless it ends in one - so the host matches exactly the paths Next does.
function nextRedirect(r, base) {
	if (!r || r.has || r.missing || typeof r.source !== "string" || typeof r.destination !== "string" || typeof r.regex !== "string") return null;
	if (![301, 302, 303, 307, 308].includes(r.statusCode)) return null;
	const from = r.source;
	if (!from.startsWith("/") || from.length > MAX_FROM) return null;
	const trailing = from.length > 1 && from.endsWith("/");
	const segs = from === "/" ? [] : from.slice(1, trailing ? -1 : undefined).split("/");
	const params = new Map();
	let body = "";
	for (const [i, s] of segs.entries()) {
		const p = /^:(\w+)([*+])?$/.exec(s);
		if (p) {
			if (params.has(p[1]) || (p[2] && i !== segs.length - 1)) return null;
			params.set(p[1], p[2] || "");
			body += p[2] === "*" ? "(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))?" : p[2] === "+" ? "(?:/((?:[^/]+?)(?:/(?:[^/]+?))*))" : "(?:/([^/]+?))";
		} else if (/^[A-Za-z0-9_~-][A-Za-z0-9._~-]*$/.test(s)) {
			body += "/" + s.replace(/[.]/g, "\\.");
		} else return null;
	}
	// a path under <base>/_next is never Next's to redirect (but internally):
	// the host's pattern can't say so, so its segments to there are literal,
	// and none is _next
	if (!r.internal) {
		const n = base ? base.split("/").length - 1 : 0;
		for (let i = 0; i <= n && i < segs.length; i++) {
			if (segs[i].startsWith(":")) return null;
			if (i === n && segs[i].toLowerCase().startsWith("_next")) return null;
		}
	}
	const want = "^" + (r.internal ? "" : "(?!" + base + "/_next)") + (body || "/") + (trailing ? "/$" : "(?:/)?$");
	if (want !== r.regex) return null;
	// the destination: a path or an http(s) URL with a host, its params where
	// the source has them (a :name* after a /), nothing path-to-regexp or a
	// URL would read another way
	let to = r.destination, origin = "";
	const abs = /^(https?:\/\/[a-z0-9]([a-z0-9.-]*[a-z0-9])?)(\/.*)?$/.exec(to);
	if (abs) {
		origin = abs[1];
		to = abs[3] || "/"; // new URL's: https://example.com is https://example.com/
	} else if (/^[a-z][a-z0-9+.-]*:/i.test(to)) return null;
	if (!to.startsWith("/") || to.length > MAX_TO) return null;
	for (const t of to.match(/:\w+[*+]?|[^:]+|:/g) || []) {
		if (t.startsWith(":")) {
			const p = /^:(\w+)([*+]?)$/.exec(t);
			if (!p || !params.has(p[1]) || params.get(p[1]) !== p[2]) return null;
		} else if (!/^[A-Za-z0-9._~/-]+$/.test(t)) return null;
	}
	if (/(^|\/)\.\.?(\/|$)/.test(to.replace(/:\w+[*+]?/g, "x")) || /:\w+\*/.test(to.replace(/\/:\w+\*/g, ""))) return null;
	// Next's router matches literals in any case (without
	// caseSensitiveRoutes, which stops the list) and router-server.js sends
	// Refresh with a 308: routes.json says so, the host knows no framework
	return { from, to: origin + to, status: r.statusCode, caseless: true, ...(r.statusCode === 308 && { refresh: true }) };
}

// react-router-serve and remix-serve (Vite builds): express.static on the
// client build's assets/ at publicPath, a year and immutable, before the
// app - the server build's own exports say where. Remix's classic
// compiler, and React Router's RSC build: the app's.
function reactRouter(server) {
	let s;
	try {
		s = fs.readFileSync(server, "utf8");
	} catch {
		throw new Error("no server build at " + server);
	}
	// Vite 7 declares them with const, Vite 8's Rolldown with var
	const str = (name) => {
		const m = new RegExp("^(?:const|var) " + name + ' = ("(?:[^"\\\\]|\\\\.)*");$', "m").exec(s);
		try {
			return m ? JSON.parse(m[1]) : null;
		} catch {
			return null;
		}
	};
	const dir = str("assetsBuildDirectory"), pub = str("publicPath");
	if (dir === null || pub === null || !/^\//.test(pub)) throw new Error("a server build this doesn't know (" + server + ")");
	if (!/(^|\/)client$/.test(dir.replace(/\\/g, "/"))) throw new Error("not a Vite build (its assets are in " + dir + ")");
	const assets = path.join(dir, "assets");
	if (!isDir(assets)) throw new Error("no " + assets);
	const t = new Tree(); // express.static: no dotfiles
	const at = pub.replace(/\/+$/, "") + "/assets/";
	for (const f of walk(assets)) t.put(at + f, path.join(assets, f), true);
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
const servers = { sveltekit: "adapter-node", astro: "Astro's node adapter", next: "Next.js standalone", "react-router": "react-router-serve", remix: "remix-serve" };
let wrote = false;
try {
	const kinds = { files, sveltekit, astro, next, "react-router": reactRouter, remix: reactRouter };
	const t = Object.hasOwn(kinds, kind) ? kinds[kind](dir) : null;
	if (!t) throw new Error("no framework " + kind);
	const n = t.write();
	const e = edgeMarker(t, marker);
	wrote = !!e;
	const as = kind === "files" ? "as its files manifest (" + dir + ") says its server serves them" : `as ${servers[kind]} serves them`;
	say(`served without the app, before any of its code runs (${as}): ${n.pages} prerendered page(s), ${n.files} file(s), ${n.immutable} immutable asset(s)`);
	if (n.headers) say(`  with the headers ${kind === "files" ? "its manifest" : "its config"} gives them: ${n.headers} of them`);
	if (n.redirects) say(`  and its config's redirects, answered without the app: ` + t.redirects.slice(0, 10).map((r) => `${r.from} -> ${r.to} (${r.status})`).join(", ") + (n.redirects > 10 ? ` (and ${n.redirects - 10} more)` : ""));
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
