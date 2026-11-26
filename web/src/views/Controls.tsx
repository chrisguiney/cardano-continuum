import { useState } from "preact/hooks";
import { buildControl, postControl, type ControlBody, type ControlInput, type Op } from "../lib/controls";

interface Pending {
  body: ControlBody;
  text: string;
}

const describe = (b: ControlBody): string => {
  switch (b.op) {
    case "pause": return "Pause the run?";
    case "resume": return "Resume the run?";
    case "set_k": return `Set the amplification factor k to ${b.k}?`;
    case "set_weights": return `Override intent weights (${Object.entries(b.weights ?? {}).map(([k, v]) => `${k}=${v}`).join(", ")})?`;
    case "inject": return `Inject scenario "${b.scenario}"?`;
  }
};

export function Controls() {
  const [input, setInput] = useState<ControlInput>({ k: "1", weights: "", scenario: "" });
  const [pending, setPending] = useState<Pending | null>(null);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const ask = (op: Op) => {
    const built = buildControl(op, input);
    if (!built.ok) {
      setPending(null);
      setResult({ ok: false, text: built.error });
      return;
    }
    setResult(null);
    setPending({ body: built.body, text: describe(built.body) });
  };

  const confirm = async () => {
    if (!pending) return;
    setBusy(true);
    try {
      const r = await postControl(pending.body);
      setResult({ ok: true, text: r.duplicate ? `Already applied (log seq ${r.seq}).` : `Accepted (log seq ${r.seq}).` });
    } catch (e) {
      setResult({ ok: false, text: e instanceof Error ? e.message : String(e) });
    } finally {
      setPending(null);
      setBusy(false);
    }
  };

  return (
    <>
      <h1>Controls</h1>
      <p class="muted">Each action is appended to the event log, so the run stays replayable. Every action asks for confirmation.</p>
      <section class="card controls">
        <div class="row">
          <button type="button" onClick={() => ask("pause")}>Pause</button>
          <button type="button" onClick={() => ask("resume")}>Resume</button>
        </div>
        <div class="row">
          <label>k (1–1000)<input type="text" inputMode="numeric" value={input.k} onInput={(e) => setInput({ ...input, k: e.currentTarget.value })} /></label>
          <button type="button" onClick={() => ask("set_k")}>Set k</button>
        </div>
        <div class="row">
          <label>Weights (name=value, …)<input type="text" value={input.weights} placeholder="send=2, vote=0.5" onInput={(e) => setInput({ ...input, weights: e.currentTarget.value })} /></label>
          <button type="button" onClick={() => ask("set_weights")}>Set weights</button>
        </div>
        <div class="row">
          <label>Scenario<input type="text" value={input.scenario} placeholder="partition" onInput={(e) => setInput({ ...input, scenario: e.currentTarget.value })} /></label>
          <button type="button" onClick={() => ask("inject")}>Inject</button>
        </div>
      </section>
      {pending && (
        <div class="confirm" role="alertdialog" aria-label="Confirm control action">
          <p><strong>{pending.text}</strong></p>
          <button type="button" class="danger" disabled={busy} onClick={() => void confirm()}>Confirm</button>
          <button type="button" class="ghost" disabled={busy} onClick={() => setPending(null)}>Cancel</button>
        </div>
      )}
      {result && <p class={result.ok ? "banner ok" : "banner bad"} role="status">{result.text}</p>}
    </>
  );
}
