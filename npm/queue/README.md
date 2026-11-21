# @homeport-sh/queue

pg-boss on [homeport.sh](https://homeport.sh), drained on demand: no worker
running all day. homeport runs the app's drain in a sandbox of its own when
there may be work. The drain works the queue until it's empty, exits, and
says when to drain again.

```ts
// src/commands/queue:drain.ts (rsc-kit), or a "queue:drain" script's file
import { PgBoss } from "pg-boss";
import { drain } from "@homeport-sh/queue";

const handlers = {
  email: async (jobs) => {
    for (const job of jobs) await sendEmail(job.data);
  },
};

export default async () => {
  const boss = new PgBoss(process.env.DATABASE_URL!);
  await boss.start();
  await drain(boss, { handlers });
  await boss.stop();
};
```

`drain(boss, { handlers })` runs pg-boss's own `drain()` (pg-boss 12.39.0
or later): a `work()` worker per queue a handler is given for, back to
back until none finds a ready job, then the workers stopped. A batch is
completed when its handler returns, and failed (pg-boss retries it as the
queue says) when it throws. When a pass finds nothing, or its time is up,
it prints one line, last:

```
homeport-queue: processed=12 failed=1 waiting=0 next_due=1793481600
```

`waiting` is the jobs ready now and `next_due` the earliest one due later
(a delayed job, a retry's backoff), read from pg-boss's job table. homeport
runs the drain with `--max-time=<seconds>` and drain() stops fetching then,
letting the batches still running finish.
Options: `queues`, `maxSeconds`, `schema` (pg-boss's, `pgboss` by default),
`batchSize` (10), `signal`.

pg-boss runs `schedule()` jobs only while a worker runs: drained on demand,
make them homeport cron jobs instead.

`retryFailed(boss, queues)` retries the queues' failed jobs: a
`queue:retry` script or command that calls it is what `homeport queue
retry-failed` runs.

`kick()` tells homeport there's work, after a `send()`, for a drain in
seconds instead of at the next check. It posts to `HOMEPORT_QUEUE_KICK_URL`
and never throws.
