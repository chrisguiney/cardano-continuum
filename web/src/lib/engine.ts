/** EngineStatus is /api/_ui/status: what the UI proxy knows of the engine. */
export interface EngineStatus {
  engine: "up" | "starting" | "unreachable";
  /** since is when engine last changed, on the proxy's clock. */
  since: string;
  /** now is the proxy's clock when it answered. */
  now: string;
  last_error?: string;
}

/**
 * Phase is where the page stands with the engine:
 *  - checking: no answer yet;
 *  - starting: the engine has not answered since the proxy began, so
 *    failures are expected and shown as a banner, not as errors;
 *  - up: the engine has answered, so a failure is a genuine error;
 *  - unreachable: it answered once and stopped;
 *  - unavailable: this server has no status route (continuum serve), so
 *    every failure is a genuine error.
 */
export type Phase = "checking" | "starting" | "up" | "unreachable" | "unavailable";

/** nextPhase folds one status answer, or "missing" when it could not be read, into the phase. */
export function nextPhase(prev: Phase, res: EngineStatus | "missing"): Phase {
  if (res === "missing") return prev === "checking" ? "unavailable" : prev;
  if (res.engine === "up") return "up";
  if (res.engine === "unreachable") return "unreachable";
  // The proxy restarted under a page that had seen the engine up.
  return prev === "up" || prev === "unreachable" ? "unreachable" : "starting";
}

/** quiet is whether a failed request is expected, so views show empty states rather than errors. */
export const quiet = (p: Phase): boolean => p === "checking" || p === "starting";

/** waitedText formats a wait of ms milliseconds, "2 min 5 s". */
export function waitedText(ms: number): string {
  // A status without parseable times gives NaN: say nothing rather than "NaN s".
  if (!Number.isFinite(ms)) return "";
  const s = Math.max(0, Math.floor(ms / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
}
