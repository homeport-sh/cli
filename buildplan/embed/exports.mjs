// exports.mjs <bundle> <installed>: a traced bundle's packages, made whole
// for Node's require(). Node 22.12 and later require() a package's
// "module-sync" file where its exports have one; a tracer (Next.js
// standalone's) that followed "default" shipped that file and not this
// one, and the app can't start. Each such package is put back whole (its
// own node_modules aside) from what the build installed at the same place,
// the same version; one that can't be fails the build, saying which.
import fs from "node:fs";
import path from "node:path";

const [bundle, installed] = process.argv.slice(2);
const withSync = ["node", "require", "module-sync", "default"];
const without = ["node", "require", "default"];

// pick is what an exports value resolves to under conditions ("" none).
function pick(v, conds) {
  if (typeof v === "string") return v;
  if (Array.isArray(v)) {
    for (const x of v) {
      const t = pick(x, conds);
      if (t) return t;
    }
    return "";
  }
  if (v && typeof v === "object") {
    for (const [k, x] of Object.entries(v)) {
      if (conds.includes(k)) return pick(x, conds);
    }
  }
  return "";
}

// missing is what of a package Node would require() and isn't there, of
// the entries whose "default" file is (an entry traced): "./esm/x.js"…
function missing(dir, pkg) {
  let ex = pkg.exports;
  if (!ex || typeof ex !== "object" || Array.isArray(ex)) return [];
  if (!Object.keys(ex).some((k) => k.startsWith("."))) ex = { ".": ex };
  const out = [];
  for (const [sub, v] of Object.entries(ex)) {
    if (sub.includes("*")) continue; // a pattern: no one file to check
    const a = pick(v, withSync), b = pick(v, without);
    if (a && b && a !== b && fs.existsSync(path.join(dir, b)) && !fs.existsSync(path.join(dir, a))) out.push(a);
  }
  return out;
}

function read(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

// packages is every package folder under root's node_modules, nested ones
// too (links not followed).
function* packages(root) {
  let ents;
  try {
    ents = fs.readdirSync(root, { withFileTypes: true });
  } catch {
    return;
  }
  for (const e of ents) {
    if (!e.isDirectory()) continue;
    const p = path.join(root, e.name);
    if (e.name === "node_modules") {
      for (const q of fs.readdirSync(p, { withFileTypes: true })) {
        if (!q.isDirectory() || q.name.startsWith(".")) continue;
        const qp = path.join(p, q.name);
        if (q.name.startsWith("@")) {
          for (const s of fs.readdirSync(qp, { withFileTypes: true })) {
            if (s.isDirectory()) yield* pkgAt(path.join(qp, s.name));
          }
        } else yield* pkgAt(qp);
      }
    } else yield* packages(p);
  }
}

function* pkgAt(dir) {
  yield dir;
  yield* packages(dir);
}

let failed = false;
for (const dir of packages(bundle)) {
  const pkg = read(path.join(dir, "package.json"));
  if (!pkg) continue;
  const lost = missing(dir, pkg);
  if (!lost.length) continue;
  const id = `${pkg.name}@${pkg.version}`;
  const from = path.join(installed, path.relative(bundle, dir));
  const had = read(path.join(from, "package.json"));
  if (had && had.version === pkg.version && lost.every((f) => fs.existsSync(path.join(from, f)))) {
    fs.cpSync(from, dir, {
      recursive: true,
      dereference: true,
      filter: (src) => path.relative(from, src).split(path.sep)[0] !== "node_modules",
    });
    console.log(`homeport: ${id} put back whole: Node requires ${lost.join(", ")} of it, which the build's trace left out`);
    continue;
  }
  failed = true;
  console.error(`homeport: ${id} at ${path.relative(bundle, dir)}: Node requires ${lost.join(", ")} of it ("module-sync"), which the build didn't ship and couldn't find where it installed it`);
}
if (failed) process.exit(1);
