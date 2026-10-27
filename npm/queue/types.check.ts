// Type checks, run by `tsc` in CI: what a handler is given follows the boss.
import { drain, type Boss, type Job } from "./index.js";

const rest: Omit<Boss, "fetch"> = {
  complete: async () => {},
  fail: async () => {},
  retry: async () => {},
  getDb: () => ({ executeSql: async () => ({ rows: [] }) }),
};

// pg-boss 12's fetch() gives each job its retryCount: a handler reads it.
const v12 = { ...rest, fetch: async (): Promise<(Job & { retryCount: number })[]> => [] };
void drain(v12, {
  handlers: {
    resize: (jobs) => jobs.map((j) => j.retryCount.toFixed()),
  },
});

// pg-boss 11's has none: still a boss drain() takes.
const v11 = { ...rest, fetch: async (): Promise<Job[]> => [] };
void drain(v11, { handlers: { resize: (jobs) => jobs.map((j) => j.id) } });

// @ts-expect-error a job without a retryCount doesn't claim one
void drain(v11, { handlers: { resize: (jobs) => jobs.map((j) => j.retryCount.toFixed()) } });
