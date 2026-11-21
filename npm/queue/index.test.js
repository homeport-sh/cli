import { afterEach, expect, test } from "bun:test";
import { drain, kick, retryFailed } from "./index.js";

// fakeBoss is pg-boss as drain() uses it: each queue's jobs, ready or due
// later (startAfter, seconds from now), handed out batchSize at a time;
// what was completed, failed and retried; and the SQL its database ran.
function fakeBoss(queues, { now = 1793481600 } = {}) {
  const jobs = [];
  let id = 0;
  for (const [name, list] of Object.entries(queues)) {
    for (const j of list) jobs.push({ id: `j${++id}`, name, data: j.data ?? {}, state: j.state ?? "created", startAfter: now + (j.in ?? 0) });
  }
  const boss = {
    jobs,
    fetches: [],
    completed: [],
    failed: [],
    retried: [],
    sql: [],
    workers: [],
    clock: 0, // ms, moved by fetchMs on each fetch
    fetchMs: 0,
    async fetch(name, opts) {
      if (boss.fetchFails) throw new Error(boss.fetchFails);
      boss.fetches.push([name, opts?.batchSize]);
      boss.clock += boss.fetchMs;
      const ready = jobs.filter((j) => j.name === name && (j.state === "created" || j.state === "retry") && j.startAfter <= now);
      const batch = ready.slice(0, opts?.batchSize ?? 1);
      for (const j of batch) j.state = "active";
      return batch.map(({ id, name, data }) => ({ id, name, data }));
    },
    // work() as pg-boss's: a worker of the queue, its handler given each
    // batch, which it completes or fails whole
    async work(name, opts, handler) {
      if (!queues[name]) throw new Error(`Queue ${name} not found`);
      const w = { id: `w${boss.workers.length + 1}`, name, batchSize: opts?.batchSize, handler, stopped: false, running: null };
      boss.workers.push(w);
      return w.id;
    },
    async offWork(name, opts) {
      for (const w of boss.workers.filter((w) => w.name === name && (!opts?.id || w.id === opts.id))) {
        w.stopped = true;
        if (opts?.wait && w.running) await w.running;
      }
    },
    // drain() as pg-boss 12.39's: the workers back to back until a pass
    // finds nothing, or timeoutSeconds is up (the batch running then goes on)
    async drain(options) {
      boss.drains.push(options);
      const over = () => options?.timeoutSeconds !== undefined && boss.clock >= options.timeoutSeconds * 1000;
      for (let got = 1; got > 0; ) {
        got = 0;
        for (const w of boss.workers) {
          for (;;) {
            await new Promise((r) => setTimeout(r, 0));
            if (w.stopped || over()) break;
            const batch = await boss.fetch(w.name, { batchSize: w.batchSize });
            if (!batch.length) break;
            got += batch.length;
            const ids = batch.map((j) => j.id);
            w.running = (async () => {
              try {
                await w.handler(batch);
                await boss.complete(w.name, ids);
              } catch (err) {
                await boss.fail(w.name, ids, { message: String(err?.message ?? err) });
              }
            })();
            await w.running;
            w.running = null;
          }
        }
        if (over()) return { timedOut: true };
      }
      return { timedOut: false };
    },
    drains: [],
    async complete(name, ids) {
      for (const i of [].concat(ids)) jobs.find((j) => j.id === i).state = "completed";
      boss.completed.push([name, [].concat(ids)]);
    },
    async fail(name, ids, data) {
      for (const i of [].concat(ids)) jobs.find((j) => j.id === i).state = "failed";
      boss.failed.push([name, [].concat(ids), data]);
    },
    async retry(name, ids) {
      for (const i of [].concat(ids)) jobs.find((j) => j.id === i).state = "retry";
      boss.retried.push([name, [].concat(ids)]);
    },
    getDb() {
      return {
        async executeSql(text, values) {
          boss.sql.push([text, values]);
          if (boss.sqlFails) throw new Error("connection lost");
          const mine = jobs.filter((j) => values[0].includes(j.name));
          if (/state = 'failed'/.test(text)) return { rows: mine.filter((j) => j.state === "failed").map(({ id, name }) => ({ id, name })) };
          const pending = mine.filter((j) => j.state === "created" || j.state === "retry");
          const later = pending.filter((j) => j.startAfter > now).map((j) => j.startAfter);
          return { rows: [{ waiting: String(pending.filter((j) => j.startAfter <= now).length), next_due: later.length ? String(Math.min(...later)) : null }] };
        },
      };
    },
  };
  return boss;
}

const quiet = () => {
  const lines = [];
  return { lines, log: (l) => lines.push(l) };
};

test("drain works each queue until a pass finds nothing, then prints the line", async () => {
  const boss = fakeBoss({ email: [{}, {}, {}], resize: [{ data: { bad: true } }, {}, { in: 3600 }] });
  const seen = [];
  const out = quiet();
  const counts = await drain(boss, {
    handlers: {
      email: async (jobs) => seen.push(["email", jobs.length]),
      resize: async (jobs) => {
        if (jobs.some((j) => j.data.bad)) throw new Error("bad image");
      },
    },
    batchSize: 2,
    log: out.log,
  });
  expect(counts).toEqual({ processed: 3, failed: 2, waiting: 0, nextDue: 1793485200 });
  expect(out.lines).toEqual(["homeport-queue: processed=3 failed=2 waiting=0 next_due=1793485200"]);
  expect(seen).toEqual([["email", 2], ["email", 1]]);
  // a batch is completed or failed whole, with the error
  expect(boss.completed).toEqual([["email", ["j1", "j2"]], ["email", ["j3"]]]);
  expect(boss.failed.map(([n, ids, d]) => [n, ids, d.message])).toEqual([["resize", ["j4", "j5"], "bad image"]]);
  expect(boss.fetches.every(([, b]) => b === 2)).toBe(true);
  // pg-boss's own drain() ran the workers, and they're stopped after
  expect(boss.drains).toEqual([{ timeoutSeconds: 540 }]);
  expect(boss.workers.map((w) => [w.name, w.stopped])).toEqual([["email", true], ["resize", true]]);
  // the count reads pg-boss's job table, of these queues
  const [text, values] = boss.sql[0];
  expect(text).toContain("from pgboss.job");
  expect(values).toEqual([["email", "resize"]]);
});

test("drain stops at its time, and says what's still waiting", async () => {
  const boss = fakeBoss({ email: [{}, {}, {}, {}] });
  boss.fetchMs = 3000; // each fetch takes 3s
  const out = quiet();
  const counts = await drain(boss, {
    handlers: { email: async () => {} },
    maxSeconds: 5,
    batchSize: 1,
    log: out.log,
  });
  expect(boss.drains).toEqual([{ timeoutSeconds: 5 }]);
  expect(counts.processed).toBeLessThan(4);
  expect(counts.waiting).toBe(4 - counts.processed);
  expect(out.lines.at(-1)).toBe(`homeport-queue: processed=${counts.processed} failed=0 waiting=${counts.waiting} next_due=0`);
});

test("drain takes --max-time from its args, as homeport runs it", async () => {
  const argv = process.argv;
  process.argv = [...argv.slice(0, 2), "queue:drain", "--max-time=0"];
  try {
    const boss = fakeBoss({ email: [{}] });
    const counts = await drain(boss, { handlers: { email: async () => {} }, log: () => {} });
    expect(counts).toEqual({ processed: 0, failed: 0, waiting: 1, nextDue: 0 });
    expect(boss.workers).toEqual([]);
    expect(boss.drains).toEqual([]);
  } finally {
    process.argv = argv;
  }
});

test("drain stops when its signal aborts", async () => {
  const boss = fakeBoss({ email: [{}, {}, {}] });
  const ac = new AbortController();
  const counts = await drain(boss, {
    handlers: { email: async () => ac.abort() },
    batchSize: 1,
    signal: ac.signal,
    log: () => {},
  });
  expect(counts.processed).toBe(1);
  expect(counts.waiting).toBe(2);
});

test("drain works only the queues given, and needs a handler for each", async () => {
  const boss = fakeBoss({ email: [{}], resize: [{}] });
  const counts = await drain(boss, { handlers: { email: async () => {}, resize: async () => {} }, queues: ["resize"], log: () => {} });
  expect(counts.processed).toBe(1);
  expect(boss.fetches.map(([n]) => n)).not.toContain("email");
  await expect(drain(boss, { handlers: { email: async () => {} }, queues: ["resize"], log: () => {} })).rejects.toThrow(/resize/);
  await expect(drain(boss, { handlers: {}, log: () => {} })).rejects.toThrow(/handler/);
});

test("drain prints its line even when the queue can't be read, or a fetch throws", async () => {
  const boss = fakeBoss({ email: [{}] });
  boss.sqlFails = true;
  const out = quiet();
  const errs = [];
  const counts = await drain(boss, { handlers: { email: async () => {} }, log: out.log, warn: (m) => errs.push(m) });
  expect(counts).toEqual({ processed: 1, failed: 0, waiting: 0, nextDue: 0 });
  expect(errs.join()).toContain("connection lost");

  const broken = fakeBoss({ email: [{}] });
  const lines = quiet();
  await expect(drain(broken, { handlers: { email: async () => {}, gone: async () => {} }, log: lines.log })).rejects.toThrow("Queue gone not found");
  expect(lines.lines).toEqual(["homeport-queue: processed=0 failed=0 waiting=1 next_due=0"]);
  // the worker it did start is stopped
  expect(broken.workers.map((w) => [w.name, w.stopped])).toEqual([["email", true]]);
});

test("drain needs pg-boss 12.39.0 or later: its drain()", async () => {
  const old = fakeBoss({ email: [{}] });
  delete old.drain;
  await expect(drain(old, { handlers: { email: async () => {} }, log: () => {} })).rejects.toThrow(/pg-boss 12\.39\.0/);
});

test("drain counts a batch its worker took before pg-boss's drain() began", async () => {
  // a worker's first fetch can run as work() returns: pg-boss's drain()
  // counts only what's handled after it starts, the package all of it
  const boss = fakeBoss({ email: [{}, {}] });
  const work = boss.work;
  boss.work = async (name, opts, handler) => {
    const id = await work(name, opts, handler);
    const batch = await boss.fetch(name, opts);
    await handler(batch);
    await boss.complete(name, batch.map((j) => j.id));
    return id;
  };
  const counts = await drain(boss, { handlers: { email: async () => {} }, batchSize: 1, log: () => {} });
  expect(counts.processed).toBe(2);
});

test("drain reads its schema's table, and refuses one that isn't a name", async () => {
  const boss = fakeBoss({ email: [] });
  await drain(boss, { handlers: { email: async () => {} }, schema: "jobs_v2", log: () => {} });
  expect(boss.sql[0][0]).toContain("from jobs_v2.job");
  await expect(drain(boss, { handlers: { email: async () => {} }, schema: "x; drop table y", log: () => {} })).rejects.toThrow(/schema/);
});

test("retryFailed retries each queue's failed jobs", async () => {
  const boss = fakeBoss({ email: [{ state: "failed" }, {}, { state: "failed" }], resize: [{ state: "failed" }], other: [{ state: "failed" }] });
  expect(await retryFailed(boss, ["email", "resize"])).toBe(3);
  expect(boss.retried).toEqual([["email", ["j1", "j3"]], ["resize", ["j4"]]]);
  expect(await retryFailed(boss, ["email"])).toBe(0);
});

const kickURL = process.env.HOMEPORT_QUEUE_KICK_URL;
afterEach(() => {
  if (kickURL === undefined) delete process.env.HOMEPORT_QUEUE_KICK_URL;
  else process.env.HOMEPORT_QUEUE_KICK_URL = kickURL;
});

test("kick posts to the environment's kick URL, and never throws", async () => {
  const got = [];
  const server = Bun.serve({
    port: 0,
    fetch(req) {
      got.push([req.method, new URL(req.url).pathname]);
      return new Response(null, { status: req.url.endsWith("/down") ? 503 : 202 });
    },
  });
  try {
    delete process.env.HOMEPORT_QUEUE_KICK_URL;
    expect(await kick()).toBe(false); // none: nothing to tell
    process.env.HOMEPORT_QUEUE_KICK_URL = `http://localhost:${server.port}/k/tok`;
    expect(await kick()).toBe(true);
    process.env.HOMEPORT_QUEUE_KICK_URL = `http://localhost:${server.port}/down`;
    expect(await kick()).toBe(false);
    process.env.HOMEPORT_QUEUE_KICK_URL = "http://localhost:1/nothing";
    expect(await kick()).toBe(false);
    expect(got).toEqual([["POST", "/k/tok"], ["POST", "/down"]]);
  } finally {
    server.stop(true);
  }
});
