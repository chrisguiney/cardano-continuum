import { useCallback, useEffect, useState, type ReactNode } from "react";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Blurb, HelpTip, SelectField, TextField } from "./fields";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { useEnums, type ScenarioInfo } from "../lib/meta";
import { buildControl, formFromState, postControl, type ControlBody, type ControlInput, type ControlState, type Op } from "../lib/controls";
import { getJSON } from "../lib/api";
import { Banner, Note, Panel, Title } from "./common";

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
    case "clear_weights": return "Reset every intent weight to its profile's own? This drops the override.";
    case "inject": return `Inject scenario "${b.scenario}"?`;
  }
};

/** ScenarioTable lists every scenario with its argument and what it does. The name and argument columns have fixed widths and every cell wraps, so a long image reference breaks inside the card instead of widening it. */
export function ScenarioTable({ list }: { list: ScenarioInfo[] }) {
  return (
    <Table className="mt-3 table-fixed">
      <TableCaption className="sr-only">Scenarios</TableCaption>
      <TableHeader>
        <TableRow>
          <TableHead className="w-36">Name</TableHead>
          <TableHead className="w-56">Argument</TableHead>
          <TableHead>What it does</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {list.map((s) => (
          <TableRow key={s.name} className="align-top">
            <TableCell className="align-top whitespace-normal [overflow-wrap:anywhere]"><code className="text-[0.85em]">{s.name}</code></TableCell>
            <TableCell className="align-top whitespace-normal [overflow-wrap:anywhere]">
              {s.arg ? <>{s.arg}{s.optional ? " (optional)" : ""}{s.example && <div className="text-muted-foreground">e.g. <code className="text-[0.85em]">{s.example}</code></div>}</> : <span className="text-muted-foreground">none</span>}
            </TableCell>
            <TableCell className="align-top whitespace-normal [overflow-wrap:anywhere]">{s.description}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/**
 * WeightGrid is one number field per intent kind; an empty field keeps the
 * profile's weight. current is the override the engine has in force, shown
 * beside each kind it covers so an edited field can be told from the
 * applied value.
 */
export function WeightGrid({ names, values, current = {}, onChange }: { names: string[]; values: Record<string, string>; current?: Record<string, number>; onChange: (name: string, v: string) => void }) {
  return (
    <div className="mb-3 grid grid-cols-[repeat(auto-fill,minmax(min(16rem,100%),1fr))] gap-x-3 gap-y-1.5">
      {names.map((name) => (
        <div key={name} className="flex items-center gap-2 text-sm">
          <label htmlFor={`c-w-${name}`}>{name}</label>
          {current[name] !== undefined && <Badge variant="secondary" className="ml-auto" data-testid={`current-${name}`}>current {current[name]}</Badge>}
          <Input id={`c-w-${name}`} type="number" min="0" max="1000" step="any" placeholder="profile" className={`h-8 w-24 flex-none${current[name] === undefined ? " ml-auto" : ""}`} value={values[name] ?? ""} onChange={(e) => onChange(name, e.currentTarget.value)} />
        </div>
      ))}
    </div>
  );
}

/** useControlState polls the engine's applied steering; refetch asks again now. */
function useControlState(): [ControlState | null, () => void] {
  const [state, setState] = useState<ControlState | null>(null);
  const [nonce, setNonce] = useState(0);
  useEffect(() => {
    const ctl = new AbortController();
    const load = () => getJSON<ControlState>("/api/controls", ctl.signal).then(setState, () => {});
    void load();
    const t = setInterval(() => void load(), 2000);
    return () => {
      ctl.abort();
      clearInterval(t);
    };
  }, [nonce]);
  return [state, useCallback(() => setNonce((n) => n + 1), [])];
}

/** Awaiting is a posted control whose answer the form waits for, to show what the engine applied. */
interface Awaiting {
  id: string;
  op: Op;
}

/** RunState says whether the run is paused, the k in force and the last control the engine answered. */
function RunState({ s }: { s: ControlState | null }) {
  if (!s) return <Note>Reading the engine's controls…</Note>;
  return (
    <p className="my-2 flex flex-wrap items-center gap-2 text-sm" data-testid="run-state">
      <Badge variant={s.paused ? "bad" : "secondary"}>{s.paused ? "Paused" : "Running"}</Badge>
      <span>k = {s.k} in force.</span>
      {s.last && (
        <span className="text-muted-foreground">
          Last control: {s.last.op} (log seq {s.last.seq}){s.last.why ? `, not applied: ${s.last.why}` : ""}.
        </span>
      )}
    </p>
  );
}

const ROW = "my-2 flex flex-wrap items-end gap-4";

/** ControlHeading is a control card's title. */
function ControlHeading({ children }: { children: ReactNode }) {
  return <h2 className="mb-2 flex items-center text-base font-bold">{children}</h2>;
}

export function Controls() {
  const enums = useEnums();
  const [input, setInput] = useState<ControlInput>({ k: "1", weights: {}, scenario: "", arg: "" });
  const [pending, setPending] = useState<Pending | null>(null);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [engine, refetch] = useControlState();
  const [synced, setSynced] = useState(false);
  const [awaiting, setAwaiting] = useState<Awaiting | null>(null);
  const scenarios = enums?.scenarios ?? [];

  // The form starts from the engine's values, and after a post takes the
  // value the engine applied for the field that post changed; other fields
  // keep what the operator is typing.
  useEffect(() => {
    if (!engine) return;
    const form = formFromState(engine);
    if (!synced) {
      setInput((i) => ({ ...i, ...form }));
      setSynced(true);
      return;
    }
    if (!awaiting || engine.last?.request_id !== awaiting.id) return;
    if (awaiting.op === "set_k") setInput((i) => ({ ...i, k: form.k }));
    if (awaiting.op === "set_weights" || awaiting.op === "clear_weights") setInput((i) => ({ ...i, weights: form.weights }));
    setResult(engine.last.why ? { ok: false, text: `The engine did not apply it: ${engine.last.why}.` } : { ok: true, text: `Applied (log seq ${engine.last.seq}).` });
    setAwaiting(null);
  }, [engine]);
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
      if (!r.duplicate) setAwaiting({ id: pending.body.request_id, op: pending.body.op });
      refetch();
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
      <Title>Controls</Title>
      <Blurb text={VIEW_BLURB.controls!} />
      <Panel>
        <ControlHeading>Run</ControlHeading>
        <RunState s={engine} />
        <div className={ROW}>
          <Button type="button" variant="outline" aria-describedby="h-pause" onClick={() => ask("pause")}>Pause</Button>
          <Button type="button" variant="outline" aria-describedby="h-resume" onClick={() => ask("resume")}>Resume</Button>
          <HelpTip id="h-pause" label="Pause and Resume" text="Pause stops actors from acting and the chain from advancing in simulated time. Resume continues from where it stopped." />
          <span id="h-resume" className="sr-only">Resume continues a paused run.</span>
        </div>
        <div className={ROW}>
          <TextField id="c-k" help={FIELD_HELP.k!} value={input.k ?? ""} inputMode="numeric" onChange={(v) => setInput({ ...input, k: v })} />
          <Button type="button" variant="outline" onClick={() => ask("set_k")}>Set k</Button>
        </div>
      </Panel>

      <Panel>
        <ControlHeading>
          {FIELD_HELP.weights!.label} <HelpTip id="h-weights" label={FIELD_HELP.weights!.label} text={FIELD_HELP.weights!.help} />
        </ControlHeading>
        <WeightGrid names={enums?.weight ?? []} values={input.weights ?? {}} current={engine?.weights} onChange={setWeight} />
        {!enums && <Note>Loading intents…</Note>}
        <div className={ROW}>
          <Button type="button" variant="outline" onClick={() => ask("set_weights")}>Set weights</Button>
          <Button type="button" variant="outline" disabled={!engine || Object.keys(engine.weights).length === 0} onClick={() => ask("clear_weights")}>Reset weights to profile</Button>
          <Button type="button" variant="ghost" onClick={() => setInput((i) => ({ ...i, ...(engine ? formFromState(engine) : { k: "1", weights: {} }) }))}>Clear form</Button>
        </div>
      </Panel>

      <Panel>
        <ControlHeading>Scenarios</ControlHeading>
        <div className={ROW}>
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
          <Button type="button" variant="outline" disabled={!input.scenario} onClick={() => ask("inject")}>Inject</Button>
        </div>
        {chosen && <Note>{chosen.description}</Note>}
        {scenarios.length > 0 && <ScenarioTable list={scenarios} />}
      </Panel>

      <AlertDialog open={pending !== null} onOpenChange={(open) => !open && !busy && setPending(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{pending?.text}</AlertDialogTitle>
            <AlertDialogDescription>This changes the live run.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <Button type="button" variant="destructive" disabled={busy} onClick={() => void confirm()}>Confirm</Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {result && <Banner tone={result.ok ? "ok" : "bad"} role="status">{result.text}</Banner>}
    </>
  );
}
