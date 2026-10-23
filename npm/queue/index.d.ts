/** A job as pg-boss's fetch() hands it out: what a handler is given. */
export interface Job<T = unknown> {
  id: string;
  name: string;
  data: T;
}

/** What drain() uses of a PgBoss instance. */
export interface Boss {
  fetch(name: string, options?: { batchSize?: number }): Promise<Job[] | null | undefined>;
  complete(name: string, id: string | string[], data?: object): Promise<unknown>;
  fail(name: string, id: string | string[], data?: object): Promise<unknown>;
  retry(name: string, id: string | string[]): Promise<unknown>;
  getDb(): { executeSql(text: string, values?: unknown[]): Promise<{ rows: any[] }> };
}

/** A queue's handler: given a batch of its jobs, as pg-boss's work() is. Throw to fail the batch. */
export type Handler = (jobs: Job<any>[]) => Promise<unknown> | unknown;

export interface DrainOptions {
  /** Each queue's handler, by name. */
  handlers: Record<string, Handler>;
  /** The queues to work (default: each one handlers names). */
  queues?: string[];
  /** The longest the drain fetches for (default: --max-time=<n> in its args, as homeport runs it, else 540). */
  maxSeconds?: number;
  /** pg-boss's schema (default: pgboss). */
  schema?: string;
  /** Jobs fetched at a time (default: 10). */
  batchSize?: number;
  /** Stops fetching once aborted. */
  signal?: AbortSignal;
  /** Where the line goes (default: console.log). */
  log?: (line: string) => void;
  /** Where a problem reading what's left goes (default: console.error). */
  warn?: (message: string) => void;
  /** The clock, in milliseconds (default: Date.now). */
  now?: () => number;
}

export interface DrainCounts {
  processed: number;
  failed: number;
  /** Jobs ready now. */
  waiting: number;
  /** When the earliest job due later is, in Unix seconds; 0 when none is. */
  nextDue: number;
}

/**
 * Works the queues until a pass finds nothing, or its time is up, then
 * prints homeport's line: `homeport-queue: processed=<n> failed=<n> waiting=<n> next_due=<unix|0>`.
 */
export function drain(boss: Boss, options: DrainOptions): Promise<DrainCounts>;

/** Retries the queues' failed jobs; how many. */
export function retryFailed(boss: Boss, queues: string[], options?: { schema?: string }): Promise<number>;

/** Tells homeport there's work (HOMEPORT_QUEUE_KICK_URL); whether it heard. Never throws. */
export function kick(): Promise<boolean>;
