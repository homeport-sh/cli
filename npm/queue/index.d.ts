/** A job as pg-boss 12's work() hands it to a handler. */
export interface Job<T = any> {
  id: string;
  name: string;
  data: T;
  /** How many times it has been retried. */
  retryCount: number;
}

/** What drain() and retryFailed() use of a PgBoss instance (pg-boss 12.39.0 or later). */
export interface Boss {
  work(name: string, options: { batchSize: number }, handler: (jobs: Job[]) => Promise<void>): Promise<string>;
  offWork(name: string, options?: { id?: string; wait?: boolean }): Promise<void>;
  drain(options?: { timeoutSeconds?: number }): Promise<unknown>;
  retry(name: string, id: string | string[]): Promise<unknown>;
  getDb(): { executeSql(text: string, values?: unknown[]): Promise<{ rows: any[] }> };
}

/** A queue's handler: given a batch of its jobs, as pg-boss's work() is. Throw to fail the batch. */
export type Handler = (jobs: Job[]) => Promise<unknown> | unknown;

export interface DrainOptions {
  /** Each queue's handler, by name. */
  handlers: Record<string, Handler>;
  /** The queues to work (default: each one handlers names). */
  queues?: string[];
  /** The longest the drain fetches for (default: --max-time=<n> in its args, as homeport runs it, else 540). */
  maxSeconds?: number;
  /** pg-boss's schema (default: pgboss). */
  schema?: string;
  /** Jobs a worker fetches at a time (default: 10). */
  batchSize?: number;
  /** Stops the workers once aborted. */
  signal?: AbortSignal;
  /** Where the line goes (default: console.log). */
  log?: (line: string) => void;
  /** Where a problem reading what's left goes (default: console.error). */
  warn?: (message: string) => void;
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
 * Runs pg-boss's drain(): a worker per queue until a pass finds nothing, or
 * its time is up, then prints homeport's line:
 * `homeport-queue: processed=<n> failed=<n> waiting=<n> next_due=<unix|0>`.
 */
export function drain(boss: Boss, options: DrainOptions): Promise<DrainCounts>;

/** Retries the queues' failed jobs; how many. */
export function retryFailed(boss: Pick<Boss, "retry" | "getDb">, queues: string[], options?: { schema?: string }): Promise<number>;

/** Tells homeport there's work (HOMEPORT_QUEUE_KICK_URL); whether it heard. Never throws. */
export function kick(): Promise<boolean>;
