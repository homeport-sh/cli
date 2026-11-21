// @homeport-sh/queue: pg-boss on homeport, drained on demand.
//
// homeport runs the app's drain - a queue:drain script or rsc-kit command -
// in a sandbox of its own when there may be work, as
//
//   <the drain> --max-time=<seconds>
//
// drain() works the queues until a pass finds nothing (or its time is up)
// and prints one line, last, which says when to drain again:
//
//   homeport-queue: processed=<n> failed=<n> waiting=<n> next_due=<unix|0>
//
// waiting is the jobs ready now, next_due the earliest one due later (a
// delayed job, a retry's backoff), both read from pg-boss's job table.
//
// It needs pg-boss 12.39.0 or later: the draining is pg-boss's drain().

const schemaRe = /^[A-Za-z_][A-Za-z0-9_]*$/;

// a drain's longest run when its args don't say: a 10-minute drain, less
// a minute to report
const defaultMaxSeconds = 540;

function maxTimeArg(argv) {
  for (const a of argv) {
    const m = /^--max-time=(\d+)$/.exec(a);
    if (m) return Number(m[1]);
  }
  return undefined;
}

function table(schema) {
  if (!schemaRe.test(schema)) throw new Error(`@homeport-sh/queue: schema ${JSON.stringify(schema)} isn't a name`);
  return `${schema}.job`;
}

// drain() is pg-boss's own (12.39.0 and later): a worker per queue, as
// work() runs it, back to back until a pass finds nothing ready, then the
// workers stopped. What's left is read from pg-boss's job table.
export async function drain(boss, options = {}) {
  const { handlers = {}, schema = "pgboss", batchSize = 10, signal, log = console.log, warn = console.error } = options;
  const names = options.queues ?? Object.keys(handlers);
  if (names.length === 0) throw new Error("@homeport-sh/queue: drain needs a handler for each queue it works");
  for (const n of names) {
    if (typeof handlers[n] !== "function") throw new Error(`@homeport-sh/queue: no handler for the queue ${JSON.stringify(n)}`);
  }
  if (typeof boss.drain !== "function") throw new Error("@homeport-sh/queue: drain needs pg-boss 12.39.0 or later, for its drain()");
  const jobs = table(schema);
  const maxSeconds = options.maxSeconds ?? maxTimeArg(process.argv) ?? defaultMaxSeconds;

  // counted here, not from pg-boss's drain(): a worker's first batch can
  // start as work() returns, before the drain does
  let processed = 0;
  let failed = 0;
  let counts;
  const workers = [];
  const stop = () => Promise.all(workers.map(([name, id]) => boss.offWork(name, { id, wait: true })));
  const onAbort = () => {
    for (const [name, id] of workers) void boss.offWork(name, { id }).catch(() => {});
  };
  try {
    if (maxSeconds > 0 && !signal?.aborted) {
      signal?.addEventListener("abort", onAbort, { once: true });
      for (const name of names) {
        const handler = handlers[name];
        const id = await boss.work(name, { batchSize }, async (batch) => {
          try {
            await handler(batch);
          } catch (err) {
            failed += batch.length;
            throw err;
          }
          processed += batch.length;
        });
        workers.push([name, id]);
        if (signal?.aborted) onAbort();
      }
      if (!signal?.aborted) await boss.drain({ timeoutSeconds: maxSeconds });
    }
  } finally {
    signal?.removeEventListener("abort", onAbort);
    // the batches still running finish (a drain that ran out of time
    // doesn't wait for them), then nothing more is fetched
    try {
      await stop();
    } catch (err) {
      warn(`homeport: couldn't stop the queue's workers: ${err?.message ?? err}`);
    }
    let waiting = 0;
    let nextDue = 0;
    try {
      const { rows } = await boss.getDb().executeSql(
        `select count(*) filter (where start_after <= now()) as waiting,` +
          ` ceil(extract(epoch from min(start_after) filter (where start_after > now())))::bigint as next_due` +
          ` from ${jobs} where name = any($1) and state in ('created', 'retry')`,
        [names],
      );
      waiting = Number(rows[0]?.waiting ?? 0);
      nextDue = Number(rows[0]?.next_due ?? 0);
    } catch (err) {
      warn(`homeport: couldn't read what's left in the queue: ${err?.message ?? err}`);
    }
    log(`homeport-queue: processed=${processed} failed=${failed} waiting=${waiting} next_due=${nextDue}`);
    counts = { processed, failed, waiting, nextDue };
  }
  return counts;
}

export async function retryFailed(boss, queues, { schema = "pgboss" } = {}) {
  const { rows } = await boss.getDb().executeSql(`select id, name from ${table(schema)} where name = any($1) and state = 'failed'`, [queues]);
  let n = 0;
  for (const name of queues) {
    const ids = rows.filter((r) => r.name === name).map((r) => r.id);
    if (ids.length === 0) continue;
    await boss.retry(name, ids);
    n += ids.length;
  }
  return n;
}

export async function kick() {
  const url = process.env.HOMEPORT_QUEUE_KICK_URL;
  if (!url) return false;
  try {
    const res = await fetch(url, { method: "POST", signal: AbortSignal.timeout(2000) });
    return res.ok;
  } catch {
    return false;
  }
}
