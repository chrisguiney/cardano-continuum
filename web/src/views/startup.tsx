import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { InfoIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { getJSON } from "../lib/api";
import { nextPhase, waitedText, type EngineStatus, type Phase } from "../lib/engine";

interface Engine {
  phase: Phase;
  status: EngineStatus | null;
  /** at is when status was received, on this browser's clock. */
  at: number;
}

// Outside a provider (a view rendered alone) the server is assumed to have
// no startup state, so errors show as they always did.
const Ctx = createContext<Engine>({ phase: "unavailable", status: null, at: 0 });

/** useEngine is the page's view of the engine: its phase and the last status read. */
export function useEngine(): Engine {
  return useContext(Ctx);
}

/**
 * EngineProvider polls /api/_ui/status every pollMs milliseconds while the
 * engine is not known to be up, and every five times that once it is, and
 * shares the result with every view.
 */
export function EngineProvider({ pollMs = 2000, children }: { pollMs?: number; children: ReactNode }) {
  const [state, setState] = useState<Engine>({ phase: "checking", status: null, at: 0 });
  const settled = state.phase === "up" || state.phase === "unavailable";
  useEffect(() => {
    const ctl = new AbortController();
    const tick = () =>
      getJSON<EngineStatus>("/api/_ui/status", ctl.signal).then(
        (status) => setState((s) => ({ phase: nextPhase(s.phase, status), status, at: Date.now() })),
        () => {
          if (!ctl.signal.aborted) setState((s) => ({ phase: nextPhase(s.phase, "missing"), status: s.status, at: s.at }));
        },
      );
    void tick();
    const t = setInterval(() => void tick(), settled ? pollMs * 5 : pollMs);
    return () => {
      ctl.abort();
      clearInterval(t);
    };
  }, [pollMs, settled]);
  return <Ctx.Provider value={state}>{children}</Ctx.Provider>;
}

/** StartupBanner is the one notice shown while the devnet starts; it clears when the engine answers. */
export function StartupBanner() {
  const { phase, status, at } = useEngine();
  const [, tick] = useState(0);
  useEffect(() => {
    if (phase !== "starting") return;
    const t = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [phase]);
  if (phase !== "starting" || !status) return null;

  const base = Date.parse(status.now) - Date.parse(status.since);
  const waited = base + (Date.now() - at);
  return (
    <Alert role="status" className="my-4">
      <InfoIcon aria-hidden="true" />
      <AlertTitle>The devnet is starting.</AlertTitle>
      <AlertDescription>
        <p>
          Everything is working — the simulation engine begins once the first blocks are forged (usually a few minutes). Data will appear here automatically.
        </p>
        {status.engine && waitedText(waited) && (
          <p>
            Engine: {status.engine}, waiting {waitedText(waited)}.
          </p>
        )}
      </AlertDescription>
    </Alert>
  );
}
