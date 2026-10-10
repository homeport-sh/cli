// homeport's server for a TanStack Start app built without Nitro, whose
// dist/server/server.js is a fetch handler and no server. srvx (TanStack's
// own) serves it, the same on Node and Bun: dist/client's files first -
// the hashed ones under assets/ cached for good - then every other request
// to the handler. The edge terminates TLS and is all that reaches the app,
// so the request's URL is the browser's, from its X-Forwarded-Proto and
// X-Forwarded-Host (trustProxy). On PORT and HOST.
//
// gen.sh bundles this with srvx, once per runtime, into node.mjs and
// bun.mjs; the build writes the one the app runs on into its bundle, as
// .homeport/tanstack-start.mjs. Edit this, then run gen.sh.
import { serve } from "srvx";
import { staticMiddleware } from "srvx/static";

const handler = (await import(new URL("../dist/server/server.js", import.meta.url).href)).default;
const dir = new URL("../dist/client", import.meta.url).pathname;
const assets = staticMiddleware({ dir, maxAge: 31536000, immutable: true });
const files = staticMiddleware({ dir, maxAge: 0 });

serve({
  port: Number(process.env.PORT) || 3000,
  hostname: process.env.HOST || "0.0.0.0",
  trustProxy: true,
  middleware: [(req, next) => (new URL(req.url).pathname.startsWith("/assets/") ? assets : files)(req, next)],
  fetch: (req) => handler.fetch(req),
});
