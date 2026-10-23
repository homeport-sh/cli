import { expect, test } from "bun:test";
import { drain, retryFailed } from "./index.js";

// drain() against pg-boss itself, in a Postgres of your own:
//
//   HOMEPORT_QUEUE_TEST_DATABASE_URL=postgres://... PGBOSS_MODULE=/path/to/node_modules/pg-boss bun test
//
// The package has no dependencies, so pg-boss is the one you point at.
const url = process.env.HOMEPORT_QUEUE_TEST_DATABASE_URL;

test.skipIf(!url)("drain works pg-boss's queue and reads what's left from its table", async () => {
  const { PgBoss } = await import(process.env.PGBOSS_MODULE ?? "pg-boss");
  const schema = `hq_${Date.now()}`;
  const boss = new PgBoss({ connectionString: url, schema, supervise: false, schedule: false });
  await boss.start();
  try {
    await boss.createQueue("email");
    await boss.createQueue("resize", { retryLimit: 0 });
    await boss.send("email", { to: "a" });
    await boss.send("email", { to: "b" });
    await boss.send("resize", { bad: true });
    const later = await boss.send("email", { to: "later" }, { startAfter: 3600 });

    const lines = [];
    const counts = await drain(boss, {
      schema,
      handlers: {
        email: async () => {},
        resize: async () => {
          throw new Error("bad image");
        },
      },
      log: (l) => lines.push(l),
    });
    const job = await boss.getJobById("email", later);
    const due = Math.ceil(new Date(job.startAfter).getTime() / 1000); // at or after it, never before
    expect(counts).toEqual({ processed: 2, failed: 1, waiting: 0, nextDue: due });
    expect(lines).toEqual([`homeport-queue: processed=2 failed=1 waiting=0 next_due=${due}`]);

    // the failed job, retried, is waiting again
    expect(await retryFailed(boss, ["email", "resize"], { schema })).toBe(1);
    const again = await drain(boss, { schema, handlers: { email: async () => {}, resize: async () => {} }, maxSeconds: 0, log: () => {} });
    expect(again.waiting).toBe(1);
  } finally {
    await boss.getDb().executeSql(`drop schema ${schema} cascade`);
    await boss.stop({ graceful: false });
  }
});
