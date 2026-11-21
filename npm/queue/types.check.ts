// Type checks, run by `tsc` in CI: what drain() takes and a handler is given.
import { drain, retryFailed, type Boss } from "./index.js";

const boss: Boss = {
  work: async () => "w1",
  offWork: async () => {},
  drain: async () => ({ processed: 0, failed: 0, timedOut: false }),
  retry: async () => {},
  getDb: () => ({ executeSql: async () => ({ rows: [] }) }),
};

// a handler reads each job's retryCount, as pg-boss 12 gives it
void drain(boss, { handlers: { resize: (jobs) => jobs.map((j) => j.retryCount.toFixed() + j.data.anything) } });

// a pg-boss before 12.39.0, with no drain(), isn't one drain() takes
const { drain: _, ...old } = boss;
// @ts-expect-error no drain()
void drain(old, { handlers: { resize: () => {} } });

// retryFailed needs only retry() and getDb()
void retryFailed({ retry: boss.retry, getDb: boss.getDb }, ["resize"]);
