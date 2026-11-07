// api-server.mjs: the server an api/ folder of handler files is built into
// (buildplan/api.go decides the routes), and the step that builds it.
//
//   bun api-server.mjs --homeport-api-build <config>   (cwd: the app)
//   bun api-server.mjs --homeport-api-dev <config>     (homeport dev)
//
// <config> is base64 JSON: the routes, the middleware, the site, the
// runtime. The build writes an entry importing each handler (static
// imports, a fixed table: no path is ever made from a URL) and bundles it
// with Bun into <bundle>/.homeport/api.mjs - this file's server included,
// the app's packages left as imports - with the site's files copied to
// <bundle>/.homeport/site. Dev writes the entry alone, for a --watch.
//
// The server: web-standard handlers - a method exported by name, or a
// default export's fetch for the rest - routed by file name under /api;
// api/_middleware's default export before each matched route, a gate (a
// Response stops the request, nothing goes on); the site's files for every
// other path, as a static site serves them, else / is up and the rest a
// 404. A body is counted as it arrives (32 MiB, then 413); a handler has
// 300 s to answer (then 504, and its request's signal aborts); what it
// throws, or answers that isn't a Response, is a 500 with no detail - the
// detail goes to the logs. On Bun it's Bun.serve; on Node, node:http.
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { builtinModules } from "node:module";
import { Readable } from "node:stream";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const LIMIT = 32 << 20, TIMEOUT = 300e3;
const LATE = Symbol("late");

const json = (status, body, headers) => new Response(JSON.stringify(body) + "\n", { status, headers: { "content-type": "application/json", ...headers } });
const internal = () => json(500, { error: "internal error" });
const notFound = () =>
	new Response("<!doctype html>\n<meta charset=utf-8>\n<title>Not found</title>\n<h1>Not found</h1>\n", { status: 404, headers: { "content-type": "text/html; charset=utf-8" } });

// the routes, most specific first: per part, a name, then [id], then [...x]
function table(routes) {
	return routes
		.map(([route, file, mod]) => {
			const parts = route.split("/").slice(1).map((s) => {
				const m = /^\[(\.\.\.)?(\w+)\]$/.exec(s);
				return m ? { param: m[2], rest: !!m[1] } : { name: s };
			});
			return { file, mod, parts, rank: parts.map((p) => (p.name !== undefined ? 0 : p.rest ? 2 : 1)) };
		})
		.sort((a, b) => {
			for (let i = 0; i < Math.max(a.rank.length, b.rank.length); i++) {
				const x = a.rank[i] ?? -1, y = b.rank[i] ?? -1;
				if (x !== y) return x - y;
			}
			return 0;
		});
}

// match: a route's params from a path's raw parts, undecoded; null if not it
function match(r, segs) {
	const params = {};
	for (let i = 0; i < r.parts.length; i++) {
		const p = r.parts[i];
		if (i >= segs.length || segs[i] === "") return null;
		if (p.rest) {
			if (segs.slice(i).includes("")) return null;
			params[p.param] = segs.slice(i);
			return params;
		}
		if (p.name !== undefined ? segs[i] !== p.name : false) return null;
		if (p.param) params[p.param] = segs[i];
	}
	return segs.length === r.parts.length ? params : null;
}

// decode: a param, URL-decoded; a NUL, or an escape that isn't one, throws
function decode(v) {
	if (Array.isArray(v)) return v.map(decode);
	const d = decodeURIComponent(v);
	if (d.includes("\0")) throw new URIError("NUL");
	return d;
}

const TYPES = {
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".mjs": "text/javascript; charset=utf-8", ".json": "application/json", ".map": "application/json", ".txt": "text/plain; charset=utf-8",
	".xml": "application/xml", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf",
	".otf": "font/otf", ".wasm": "application/wasm", ".pdf": "application/pdf", ".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg",
	".webmanifest": "application/manifest+json", ".csv": "text/csv; charset=utf-8", ".md": "text/markdown; charset=utf-8",
};

// handler is the server's fetch: Request in, Response out.
export function handler({ routes, middleware, site, immutable, limit = LIMIT, timeout = TIMEOUT, log = console.error }) {
	const t = table(routes);
	const gate = middleware && middleware[1].default;
	if (middleware && typeof gate !== "function") throw new Error(middleware[0] + ": its default export isn't a function");
	const files = site && new Set(site.files);
	const fresh = immutable && new RegExp(immutable);

	// dispatch: the gate, then the file's export for the method
	async function dispatch(r, req, ctx) {
		if (gate) {
			const g = await gate(req, ctx);
			if (g instanceof Response) return g;
			if (g !== undefined && g !== null) {
				log(`api: ${middleware[0]} returned ${typeof g}, not a Response or nothing`);
				return internal();
			}
		}
		const m = req.method, mod = r.mod;
		const fetch = typeof mod.default?.fetch === "function" ? (q, c) => mod.default.fetch(q, c) : null;
		let fn = typeof mod[m] === "function" ? mod[m] : null, head = false;
		if (!fn && m === "HEAD" && typeof mod.GET === "function") (fn = mod.GET), (head = true);
		fn ??= fetch;
		if (!fn) {
			const named = METHODS.filter((x) => typeof mod[x] === "function");
			if (named.includes("GET")) named.push("HEAD");
			const allow = METHODS.filter((x) => named.includes(x) || x === "OPTIONS").join(", ");
			return m === "OPTIONS" ? new Response(null, { status: 204, headers: { allow } }) : json(405, { error: "method not allowed" }, { allow });
		}
		const res = await fn(req, ctx);
		if (!(res instanceof Response)) {
			log(`api: ${r.file}: ${m} returned ${res === null ? "null" : typeof res}, not a Response`);
			return internal();
		}
		return head ? new Response(null, res) : res;
	}

	// call: dispatch with the body counted and the clock on
	async function call(r, req, ctx) {
		const ac = new AbortController();
		let over = false, body = null;
		if (req.body && req.method !== "GET" && req.method !== "HEAD") {
			if (Number(req.headers.get("content-length")) > limit) return json(413, { error: "request body too large" });
			let n = 0;
			body = req.body.pipeThrough(
				new TransformStream({
					transform(c, ctl) {
						n += c.byteLength;
						if (n > limit) (over = true), ctl.error(new Error("the request body is over " + limit + " bytes"));
						else ctl.enqueue(c);
					},
				}),
			);
		}
		const q = new Request(req.url, { method: req.method, headers: req.headers, body, signal: AbortSignal.any([req.signal, ac.signal]), duplex: "half" });
		let clock, res;
		const late = new Promise((ok) => (clock = setTimeout(ok, timeout, LATE)));
		try {
			res = await Promise.race([dispatch(r, q, ctx), late]);
		} catch (e) {
			if (over) return json(413, { error: "request body too large" });
			log(`api: ${r.file}: ${req.method} threw:`, e);
			return internal();
		} finally {
			clearTimeout(clock);
		}
		if (res === LATE) {
			ac.abort(new DOMException(`no answer in ${timeout / 1000}s`, "TimeoutError"));
			log(`api: ${r.file}: ${req.method} gave no answer in ${timeout / 1000}s: 504, and its request's signal aborted`);
			return json(504, { error: "timeout" });
		}
		return over ? json(413, { error: "request body too large" }) : res;
	}

	// serve: a file of the site, as a static site's host serves it
	async function serve(req, url) {
		if (req.method !== "GET" && req.method !== "HEAD") return new Response("Method not allowed\n", { status: 405, headers: { allow: "GET, HEAD" } });
		let p;
		try {
			p = decodeURIComponent(url.pathname);
		} catch {
			return notFound();
		}
		const tries = p.endsWith("/") ? [p + "index.html"] : [p, p + ".html", p + "/index.html"];
		let f = tries.find((x) => files.has(x)), status = 200;
		if (!f && site.spa) f = site.spa;
		if (!f && site.notFound) (f = site.notFound), (status = 404);
		if (!f) return notFound();
		const full = path.join(site.dir, f);
		const st = await fs.promises.stat(full);
		const ext = path.extname(f).toLowerCase();
		const cache = status === 404 || ext === ".html" || ext === ".htm" ? "no-cache" : fresh && fresh.test(f) ? "public, max-age=31536000, immutable" : "public, max-age=3600";
		const headers = { "content-type": TYPES[ext] || "application/octet-stream", "content-length": String(st.size), "cache-control": cache };
		if (req.method === "HEAD") return new Response(null, { status, headers });
		const body = typeof Bun !== "undefined" ? Bun.file(full) : Readable.toWeb(fs.createReadStream(full));
		return new Response(body, { status, headers });
	}

	return async (req) => {
		const url = new URL(req.url);
		const p = url.pathname;
		if (p === "/api" || p.startsWith("/api/")) {
			if (p.endsWith("/")) return new Response(null, { status: 308, headers: { location: p.replace(/\/+$/, "") + url.search } });
			const segs = p.split("/").slice(1);
			for (const r of t) {
				const raw = match(r, segs);
				if (!raw) continue;
				const params = {};
				try {
					for (const k in raw) params[k] = decode(raw[k]);
				} catch {
					return json(400, { error: "bad request" });
				}
				return call(r, req, { params });
			}
			return json(404, { error: "not found" });
		}
		if (files) return serve(req, url);
		if (p === "/" && (req.method === "GET" || req.method === "HEAD")) return json(200, { ok: true }, { "cache-control": "no-store" });
		return notFound();
	};
}

// serve: the handler on $PORT, every address
export function serve(opts) {
	const fetch = handler(opts);
	const port = Number(process.env.PORT) || 3000;
	process.on("unhandledRejection", (e) => console.error("api: an unhandled rejection:", e));
	const error = (e) => (console.error("api:", e), internal());
	if (typeof Bun !== "undefined") {
		// no idle timeout: the handler's own clock is the limit
		Bun.serve({ port, hostname: "0.0.0.0", idleTimeout: 0, fetch, error });
	} else {
		nodeServe(fetch, port, error);
	}
	console.log(`api: ${opts.routes.length} route(s), on :${port}`);
}

// nodeServe: node:http to fetch and back
function nodeServe(fetch, port, error) {
	const server = http.createServer(async (ireq, ires) => {
		const ac = new AbortController();
		ires.on("close", () => ires.writableFinished || ac.abort());
		let res;
		try {
			const headers = new Headers();
			for (let i = 0; i < ireq.rawHeaders.length; i += 2) headers.append(ireq.rawHeaders[i], ireq.rawHeaders[i + 1]);
			const body = ireq.method === "GET" || ireq.method === "HEAD" ? null : Readable.toWeb(ireq);
			const req = new Request(new URL(ireq.url, "http://" + (ireq.headers.host || "localhost")), { method: ireq.method, headers, body, signal: ac.signal, duplex: "half" });
			res = await fetch(req);
		} catch (e) {
			res = e instanceof TypeError ? json(400, { error: "bad request" }) : error(e);
		}
		const h = {};
		for (const [k, v] of res.headers) if (k !== "set-cookie") h[k] = v;
		const cookies = res.headers.getSetCookie();
		if (cookies.length) h["set-cookie"] = cookies;
		ires.writeHead(res.status, h);
		if (!res.body || ireq.method === "HEAD") return ires.end();
		const reader = res.body.getReader();
		ires.on("close", () => reader.cancel().catch(() => {}));
		try {
			for (;;) {
				const { done, value } = await reader.read();
				if (done) break;
				if (!ires.write(value)) await new Promise((ok) => (ires.once("drain", ok), ires.once("close", ok)));
			}
			ires.end();
		} catch {
			ires.destroy();
		}
	});
	server.requestTimeout = 0; // the handler's own clock is the limit
	server.listen(port, "0.0.0.0");
}

// the build

const say = (s) => console.log(s);
const isFile = (f) => {
	try {
		return fs.statSync(f).isFile();
	} catch {
		return false;
	}
};

// copySite: the site's files, regular files only - no dotfiles (but
// .well-known), nothing of api/, and of the app's own folder (plain HTML)
// not its project files
function copySite(src, dst, own) {
	const skip = new Set(["api", "node_modules", ".homeport-bundle"]);
	if (own) for (const f of ["package.json", "bun.lock", "bun.lockb", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "homeport.yaml", "tsconfig.json", "jsconfig.json", "bunfig.toml"]) skip.add(f);
	const out = [];
	const walk = (rel) => {
		for (const e of fs.readdirSync(path.join(src, rel), { withFileTypes: true })) {
			const r = rel ? rel + "/" + e.name : e.name;
			if (!rel && skip.has(e.name)) continue;
			if (e.name.startsWith(".") && r !== ".well-known") continue;
			if (e.isDirectory()) walk(r);
			else if (e.isFile()) {
				fs.mkdirSync(path.join(dst, rel), { recursive: true });
				fs.copyFileSync(path.join(src, r), path.join(dst, r));
				out.push("/" + r);
			}
		}
	};
	walk("");
	return out.sort();
}

// entry: the module importing each handler, and serving them
function entry(c, at, site, self, abs) {
	const spec = (f) => {
		if (abs) return JSON.stringify(path.resolve(f)); // dev: the entry's in a temp folder
		const r = path.relative(path.dirname(at), path.resolve(f)).split(path.sep).join("/");
		return JSON.stringify(r.startsWith("../") ? r : "./" + r);
	};
	const lines = [`import { serve } from ${spec(self)};`];
	const routes = c.routes.map((r, i) => (lines.push(`import * as r${i} from ${spec(r.file)};`), `[${JSON.stringify(r.path)}, ${JSON.stringify(r.file)}, r${i}]`));
	if (c.middleware) lines.push(`import * as mw from ${spec(c.middleware)};`);
	const opts = [`routes: [${routes.join(", ")}]`, `immutable: ${JSON.stringify(c.immutable)}`];
	if (c.middleware) opts.push(`middleware: [${JSON.stringify(c.middleware)}, mw]`);
	if (site) lines.push(`import { fileURLToPath } from "node:url";`), opts.push(`site: { dir: fileURLToPath(new URL("./site", import.meta.url)), ...${JSON.stringify(site)} }`);
	if (c.limit) opts.push(`limit: ${c.limit}`);
	if (c.timeout) opts.push(`timeout: ${c.timeout}`);
	lines.push(`serve({ ${opts.join(", ")} });`);
	fs.writeFileSync(at, lines.join("\n") + "\n");
}

// bare: with no package.json, a package import fails the build, named
const bare = {
	name: "homeport-bare",
	setup(b) {
		b.onResolve({ filter: /^[^./]/ }, (a) => {
			if (a.kind.startsWith("entry-point") || /^(node|bun):/.test(a.path) || a.path === "bun" || builtinModules.includes(a.path)) return;
			const from = path.relative(process.cwd(), a.importer).split(path.sep).join("/");
			throw new Error(`${from} imports ${a.path}, a package, and there's no package.json: add one with a lockfile, or import only relative files and node: modules`);
		});
	},
};

async function build(c) {
	const out = path.join(c.out, ".homeport");
	// the site: the frontend's folder, or one the build made
	const dir = c.site || (c.sites || []).find((d) => isFile(path.join(d, "index.html")));
	if (!c.site && (c.sites || []).length && isFile("server")) {
		throw new Error("the build made a binary (server) and api/ has handlers: set api: false in homeport.yaml to run the binary, or build no binary");
	}
	let site = null;
	if (dir) {
		fs.rmSync(path.join(out, "site"), { recursive: true, force: true });
		const files = copySite(dir, path.join(out, "site"), dir === ".");
		const pages = files.filter((f) => f.endsWith(".html"));
		// a static site's rule: 200.html, or index.html its only page
		const spa = files.includes("/200.html") ? "/200.html" : pages.length === 1 && pages[0] === "/index.html" ? "/index.html" : null;
		site = { files, spa, notFound: !spa && files.includes("/404.html") ? "/404.html" : null };
	}
	const at = ".homeport-api-entry.mjs";
	entry(c, at, site, ".homeport-api.mjs");
	try {
		await Bun.build({ entrypoints: [path.resolve(at)], outdir: out, naming: "api.mjs", target: c.target, format: "esm", packages: c.bare ? undefined : "external", plugins: c.bare ? [bare] : [] });
	} catch (e) {
		for (const m of e.errors || [e]) {
			const at = m.position ? `${path.relative(process.cwd(), m.position.file)}:${m.position.line}: ` : "";
			console.error(`homeport: api/: ${at}${m.message || m}`);
		}
		process.exit(1);
	}
	// Bun loads a .env from its working folder: the bundle's is the app's
	// committed one, never the app's environment
	for (const f of fs.readdirSync(c.out)) if (f.startsWith(".env")) fs.rmSync(path.join(c.out, f), { recursive: true, force: true });

	const n = c.routes.length;
	say(`==> api/: ${n} handler${n === 1 ? "" : "s"}, built into a server. All are public: anyone can call them`);
	const names = c.routes.map((r) => (r.fetch && !r.methods?.length ? "*" : r.methods.join(",")) + " " + r.path);
	for (let i = 0; i < names.length; i += 3) say("    " + names.slice(i, i + 3).join("    "));
	say(c.middleware ? `    ${c.middleware} runs before each of them` : "    no api/_middleware.ts: each handler does its own auth");
	if (site) {
		say("==> pages stay files, served without waking the app; /api/* wakes it, and for now so does a path that isn't a file");
		say("==> this site now runs as an app: it has a size, sleeps when idle, and is billed by memory awake");
	}
}

// dev: the entry alone, importing the app's files, for the runtime's --watch
function dev(c) {
	fs.mkdirSync(path.dirname(c.entry), { recursive: true });
	entry(c, c.entry, null, c.self, true);
}

const [mode, arg] = process.argv.slice(2);
if (mode === "--homeport-api-build" || mode === "--homeport-api-dev") {
	const c = JSON.parse(Buffer.from(arg, "base64").toString());
	try {
		await (mode === "--homeport-api-build" ? build(c) : dev(c));
	} catch (e) {
		console.error("homeport: api/: " + (e && e.message ? e.message : e));
		process.exit(1);
	}
}
