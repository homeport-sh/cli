// homeport's server for a TanStack Start app built without Nitro, whose
// dist/server/server.js is a fetch handler and no server. srvx (TanStack's
// own) serves it, the same on Node and Bun: dist/client's files first -
// the hashed ones under assets/ cached for good - then every other request
// to the handler, on PORT and HOST.
//
// The request's URL is the browser's: its Host is the one the edge routed
// on, and the edge terminates TLS, so only the scheme comes from
// X-Forwarded-Proto. X-Forwarded-Host and X-Forwarded-For are a visitor's
// to set, so neither is read.
//
// argv[2] is the app's Vite base (/ by default): its files are under it.
// BODY_SIZE_LIMIT caps a request's body, in bytes (128 MiB).
//
// gen.sh bundles this with srvx, once per runtime, into node.mjs and
// bun.mjs; the build writes the one the app runs on into its bundle, as
// .homeport/tanstack-start.mjs. Edit this, then run gen.sh.
import { fileURLToPath } from "node:url";
import { serve } from "srvx";
import { staticMiddleware } from "srvx/static";

const handler = (await import(new URL("../dist/server/server.js", import.meta.url).href)).default;
const dir = fileURLToPath(new URL("../dist/client", import.meta.url));
const base = (process.argv[2] || "/").replace(/\/?$/, "/");
const assets = staticMiddleware({ dir, maxAge: 31536000, immutable: true });
const files = staticMiddleware({ dir, maxAge: 0 });

serve({
  port: Number(process.env.PORT) || 3000,
  hostname: process.env.HOST || "0.0.0.0",
  maxRequestBodySize: Number(process.env.BODY_SIZE_LIMIT) || 128 * 1024 * 1024,
  // no development error pages, whatever NODE_ENV says
  bun: { development: false },
  middleware: [
    (req, next) => {
      if (req.headers.get("x-forwarded-proto") === "https" && req.url.startsWith("http:"))
        Object.defineProperty(req, "url", { value: "https" + req.url.slice(4), enumerable: true, configurable: true });
      return next();
    },
    (req, next) => {
      const u = new URL(req.url);
      if (!u.pathname.startsWith(base)) return next();
      const path = "/" + u.pathname.slice(base.length);
      const sub = { method: req.method, headers: req.headers, url: u.origin + path + u.search };
      return (path.startsWith("/assets/") ? assets : files)(sub, next);
    },
  ],
  fetch: (req) => handler.fetch(req),
});
