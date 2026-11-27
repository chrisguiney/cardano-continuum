import { useState } from "react";
import { Blurb, HelpTip, SelectField, TextField } from "./fields";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { useEnums, type ScenarioInfo } from "../lib/meta";
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
    case "set_weights": return `Override intent weights (${Object.entries(b.weights ?? {}).map(([k, v]) => `${k}=${v}`).join(", ")})? This replaces any earlier override.`;
    case "inject": return `Inject scenario "${b.scenario}"?`;
  }
};

/** ScenarioTable lists every scenario with its argument and what it does. */
export function ScenarioTable({ list }: { list: ScenarioInfo[] }) {
  return (
    <table className="scenarios">
      <caption className="sr">Scenarios</caption>
      <thead><tr><th>Name</th><th>Argument</th><th>What it does</th></tr></thead>
      <tbody>
        {list.map((s) => (
          <tr key={s.name}>
            <td><code>{s.name}</code></td>
            <td>{s.arg ? <>{s.arg}{s.optional ? " (optional)" : ""}{s.example && <div className="muted">e.g. <code>{s.example}</code></div>}</> : <span className="muted">none</span>}</td>
            <td>{s.description}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** WeightGrid is one number field per intent kind; an empty field keeps the profile's weight. */
export function WeightGrid({ names, values, onChange }: { names: string[]; values: Record<string, string>; onChange: (name: string, v: string) => void }) {
  return (
    <div className="weights">
      {names.map((name) => (
        <label key={name} className="weight">
          <span>{name}</span>
          <input type="number" min="0" max="1000" step="any" placeholder="profile" value={values[name] ?? ""} onChange={(e) => onChange(name, e.currentTarget.value)} />
        </label>
      ))}
    </div>
  );
}

export function Controls() {
  const enums = useEnums();
  const [input, setInput] = useState<ControlInput>({ k: "1", weights: {}, scenario: "", arg: "" });
  const [pending, setPending] = useState<Pending | null>(null);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const scenarios = enums?.scenarios ?? [];
  const chosen = scenarios.find((s) => s.name === input.scenario);

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

  const setWeight = (name: string, v: string) => setInput((i) => ({ ...i, weights: { ...i.weights, [name]: v } }));

  return (
    <>
      <h1>Controls</h1>
      <Blurb text={VIEW_BLURB.controls!} />
      <section className="card controls">
        <h2>Run</h2>
        <div className="row">
          <button type="button" aria-describedby="h-pause" onClick={() => ask("pause")}>Pause</button>
          <button type="button" aria-describedby="h-resume" onClick={() => ask("resume")}>Resume</button>
          <HelpTip id="h-pause" label="Pause and Resume" text="Pause stops actors from acting and the chain from advancing in simulated time. Resume continues from where it stopped." />
          <span id="h-resume" className="sr">Resume continues a paused run.</span>
        </div>
        <div className="row">
          <TextField id="c-k" help={FIELD_HELP.k!} value={input.k ?? ""} inputMode="numeric" onChange={(v) => setInput({ ...input, k: v })} />
          <button type="button" onClick={() => ask("set_k")}>Set k</button>
        </div>
      </section>

      <section className="card controls">
        <h2>
          {FIELD_HELP.weights!.label} <HelpTip id="h-weights" label={FIELD_HELP.weights!.label} text={FIELD_HELP.weights!.help} />
        </h2>
        <WeightGrid names={enums?.weight ?? []} values={input.weights ?? {}} onChange={setWeight} />
        {!enums && <p className="muted">Loading intents…</p>}
        <div className="row">
          <button type="button" onClick={() => ask("set_weights")}>Set weights</button>
          <button type="button" className="ghost" onClick={() => setInput((i) => ({ ...i, weights: {} }))}>Clear form</button>
        </div>
      </section>

      <section className="card controls">
        <h2>Scenarios</h2>
        <div className="row">
          <SelectField
            id="c-scenario"
            help={FIELD_HELP.scenario!}
            value={input.scenario ?? ""}
            allLabel="Choose a scenario"
            options={scenarios.map((s) => ({ value: s.name, label: s.name }))}
            onChange={(v) => setInput({ ...input, scenario: v, arg: "" })}
          />
          {chosen?.arg && (
            <TextField
              id="c-arg"
              help={{ label: `Argument: ${chosen.arg}${chosen.optional ? " (optional)" : ""}`, help: chosen.description }}
              value={input.arg ?? ""}
              placeholder={chosen.example}
              onChange={(v) => setInput({ ...input, arg: v })}
            />
          )}
          <button type="button" disabled={!input.scenario} onClick={() => ask("inject")}>Inject</button>
        </div>
        {chosen && <p className="muted">{chosen.description}</p>}
        {scenarios.length > 0 && <ScenarioTable list={scenarios} />}
      </section>

      {pending && (
        <div className="confirm" role="alertdialog" aria-label="Confirm control action">
          <p><strong>{pending.text}</strong></p>
          <button type="button" className="danger" disabled={busy} onClick={() => void confirm()}>Confirm</button>
          <button type="button" className="ghost" disabled={busy} onClick={() => setPending(null)}>Cancel</button>
        </div>
      )}
      {result && <p className={result.ok ? "banner ok" : "banner bad"} role="status">{result.text}</p>}
    </>
  );
}
