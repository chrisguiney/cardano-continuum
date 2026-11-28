import { useState, type ReactNode } from "react";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Blurb, HelpTip, SelectField, TextField } from "./fields";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { useEnums, type ScenarioInfo } from "../lib/meta";
import { buildControl, postControl, type ControlBody, type ControlInput, type Op } from "../lib/controls";
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

/** WeightGrid is one number field per intent kind; an empty field keeps the profile's weight. */
export function WeightGrid({ names, values, onChange }: { names: string[]; values: Record<string, string>; onChange: (name: string, v: string) => void }) {
  return (
    <div className="mb-3 grid grid-cols-[repeat(auto-fill,minmax(min(16rem,100%),1fr))] gap-x-3 gap-y-1.5">
      {names.map((name) => (
        <label key={name} className="flex items-center justify-between gap-2 text-sm">
          <span>{name}</span>
          <Input type="number" min="0" max="1000" step="any" placeholder="profile" className="h-8 w-24 flex-none" value={values[name] ?? ""} onChange={(e) => onChange(name, e.currentTarget.value)} />
        </label>
      ))}
    </div>
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
      <Title>Controls</Title>
      <Blurb text={VIEW_BLURB.controls!} />
      <Panel>
        <ControlHeading>Run</ControlHeading>
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
        <WeightGrid names={enums?.weight ?? []} values={input.weights ?? {}} onChange={setWeight} />
        {!enums && <Note>Loading intents…</Note>}
        <div className={ROW}>
          <Button type="button" variant="outline" onClick={() => ask("set_weights")}>Set weights</Button>
          <Button type="button" variant="ghost" onClick={() => setInput((i) => ({ ...i, weights: {} }))}>Clear form</Button>
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
