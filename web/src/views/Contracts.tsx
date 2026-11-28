import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { unitsText, type ContractsView, type UnitStats, type ValidatorRow } from "../lib/contracts";
import { simTime } from "../lib/format";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { usePoll } from "../lib/hooks";
import { EmptyRow, ErrorNote, Facts, Heading, Link, Mono, Muted, Note, Panel, Subheading, TableFrame, Title } from "./common";
import { Blurb, HelpTip } from "./fields";

function Tip({ k, id }: { k: string; id: string }) {
  const h = FIELD_HELP[k]!;
  return <HelpTip id={id} label={h.label} text={h.help} />;
}

const avg = (s: UnitStats) => unitsText({ mem: s.avg_mem, steps: s.avg_steps });
const peak = (s: UnitStats) => unitsText({ mem: s.max_mem, steps: s.max_steps });

function Validator({ v }: { v: ValidatorRow }) {
  const id = `c-${v.name}`;
  return (
    <Panel aria-labelledby={id}>
      <Heading id={id}><Link to={`/contracts/${v.name}`}>{v.name}</Link> <Tip k="validator" id={`${id}-tip`} /></Heading>
      <Facts rows={[
        ["Source", <Mono>{v.module}</Mono>],
        ["Script hash", <Mono className="[overflow-wrap:anywhere]">{v.hash}</Mono>],
        ["Size", `${v.size} bytes`],
        ["Plutus version", v.language],
        [<>Purposes <Tip k="script_purpose" id={`${id}-p`} /></>, v.purposes.join(", ")],
        ["Parameters", v.params?.length ? v.params.join(", ") : <Muted>none</Muted>],
        [<>Datum <Tip k="datum" id={`${id}-d`} /></>, v.datum ? <Mono>{v.datum}</Mono> : <Muted>any data</Muted>],
        [<>Redeemer <Tip k="redeemer" id={`${id}-r`} /></>, v.redeemer ? <Mono>{v.redeemer}</Mono> : <Muted>any data</Muted>],
        ["Deployed instances", String(v.instances)],
        ["Runs", `${v.runs} (${v.failures} failed)`],
        [<>ExUnits used, average and peak <Tip k="exunits_used" id={`${id}-u`} /></>, v.runs ? <>{avg(v.used)}; peak {peak(v.used)}</> : <Muted>no runs yet</Muted>],
        [<>ExUnits declared, average and peak <Tip k="exunits_declared" id={`${id}-dc`} /></>, v.runs ? <>{avg(v.declared)}; peak {peak(v.declared)}</> : <Muted>no runs yet</Muted>],
      ]} />
      {v.reasons.length > 0 && (
        <>
          <Subheading>Failures</Subheading>
          <ul className="space-y-1">{v.reasons.map((r) => <li key={r.reason} className="[overflow-wrap:anywhere]"><Badge variant="bad">{r.count}</Badge> {r.reason}</li>)}</ul>
        </>
      )}
      <Subheading>Last runs</Subheading>
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Transaction</TableHead>
              <TableHead>Instance</TableHead>
              <TableHead>Purpose</TableHead>
              <TableHead>Used of declared</TableHead>
              <TableHead>Result</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {v.last.length === 0 && <EmptyRow cols={6}>No runs yet.</EmptyRow>}
            {v.last.map((r) => (
              <TableRow key={`${r.tx}${r.purpose}`}>
                <TableCell>{simTime(r.at)}</TableCell>
                <TableCell><Link to={`/explorer/txs/${r.tx}`}><Mono>{r.tx.slice(0, 12)}</Mono></Link></TableCell>
                <TableCell>{r.instance || v.name}</TableCell>
                <TableCell>{r.purpose}</TableCell>
                <TableCell className="tabular-nums">{unitsText(r.used)} of {unitsText(r.declared)}</TableCell>
                <TableCell>{r.ok ? <Badge>passed</Badge> : <><Badge variant="bad">failed</Badge> <span className="[overflow-wrap:anywhere]">{r.error ?? ""}</span></>}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableFrame>
    </Panel>
  );
}

/** ContractsBody shows every registry validator with what the run has seen of it. */
export function ContractsBody({ view }: { view: ContractsView }) {
  return (
    <>
      <Note>Compiled by {view.toolchain}. Used units are the engine's own evaluation of the transactions it submitted.</Note>
      {view.validators.map((v) => <Validator key={v.name} v={v} />)}
    </>
  );
}

export function Contracts() {
  const { data, error } = usePoll<ContractsView>("/api/contracts", 10_000);
  return (
    <>
      <Title>Contracts</Title>
      <Blurb text={VIEW_BLURB.contracts!} />
      <ErrorNote error={error} />
      {data && <ContractsBody view={data} />}
    </>
  );
}
