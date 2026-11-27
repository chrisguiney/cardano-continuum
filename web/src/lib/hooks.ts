import { useEffect, useState } from "react";
import { getJSON } from "./api";

export interface Poll<T> {
  data: T | null;
  error: string | null;
}

/**
 * usePoll fetches path now and every ms milliseconds. A failed poll keeps
 * the last good data and reports the error, and a change of path aborts the
 * request in flight so a slow old page never overwrites a newer one.
 */
export function usePoll<T>(path: string, ms: number): Poll<T> {
  const [state, setState] = useState<Poll<T>>({ data: null, error: null });
  useEffect(() => {
    const ctl = new AbortController();
    const tick = () =>
      getJSON<T>(path, ctl.signal).then(
        (data) => setState({ data, error: null }),
        (e: unknown) => {
          if (!ctl.signal.aborted) setState((s) => ({ data: s.data, error: e instanceof Error ? e.message : String(e) }));
        },
      );
    void tick();
    const t = setInterval(() => void tick(), ms);
    return () => {
      ctl.abort();
      clearInterval(t);
    };
  }, [path, ms]);
  return state;
}
