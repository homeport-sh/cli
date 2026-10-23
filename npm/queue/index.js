// @homeport/queue: pg-boss on homeport, drained on demand.
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
  if (!schemaRe.test(schema)) throw new Error(`@homeport/queue: schema ${JSON.stringify(schema)} isn't a name`);
  return `${schema}.job`;
}

export async function drain(boss, options = {}) {
  const { handlers = {}, schema = "pgboss", batchSize = 10, signal, now = Date.now, log = console.log, warn = console.error } = options;
  const names = options.queues ?? Object.keys(handlers);
  if (names.length === 0) throw new Error("@homeport/queue: drain needs a handler for each queue it works");
  for (const n of names) {
    if (typeof handlers[n] !== "function") throw new Error(`@homeport/queue: no handler for the queue ${JSON.stringify(n)}`);
  }
  const jobs = table(schema);
  const maxSeconds = options.maxSeconds ?? maxTimeArg(process.argv) ?? defaultMaxSeconds;
  const deadline = now() + maxSeconds * 1000;
  const over = () => signal?.aborted || now() >= deadline;

  let processed = 0;
  let failed = 0;
  let counts;
  try {
    for (let got = 1; got > 0 && !over(); ) {
      got = 0;
      for (const name of names) {
        while (!over()) {
          const batch = await boss.fetch(name, { batchSize });
          if (!batch?.length) break;
          got += batch.length;
          const ids = batch.map((j) => j.id);
          try {
            await handlers[name](batch);
          } catch (err) {
            await boss.fail(name, ids, { message: String(err?.message ?? err), stack: err?.stack });
            failed += batch.length;
            continue;
          }
          await boss.complete(name, ids);
          processed += batch.length;
        }
      }
    }
  } finally {
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
