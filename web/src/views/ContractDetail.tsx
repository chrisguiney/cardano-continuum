import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { NativeSelect } from "@/components/ui/native-select";
import { phaseSummary, type ContractDetail, type InstanceDetail, type Pipeline } from "../lib/contracts";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { usePoll } from "../lib/hooks";
import { navigate } from "../lib/router";
import { CodeBlock } from "./CodeBlock";
import { ErrorNote, Facts, Heading, Link, Mono, Muted, Note, Panel, Subheading, TableFrame, Title } from "./common";
import { Blurb, HelpTip } from "./fields";

function Tip({ k, id }: { k: string; id: string }) {
  const h = FIELD_HELP[k]!;
  return <HelpTip id={id} label={h.label} text={h.help} />;
}

const bytes = (n: number) => `${n.toLocaleString("en-US")} ${n === 1 ? "byte" : "bytes"}`;
const PHASE_HELP: Record<string, string> = { "Aiken source": "phase_source", UPLC: "phase_uplc", "Flat bytes": "phase_flat", "On-chain bytes": "phase_onchain" };

/** Hex shows hex digits in wrapping monospace, with the leading head digits in the accent colour. */
function Hex({ hex, head = 0 }: { hex: string; head?: number }) {
  return (
    <p className="mt-2 rounded-md border bg-muted p-2.5 font-mono text-[0.85em] [overflow-wrap:anywhere]">
      {head > 0 && <span className="text-primary" title="CBOR byte-string header">{hex.slice(0, head)}</span>}
      {hex.slice(head)}
    </p>
  );
}

function Summary({ p, source }: { p: Pipeline; source: string }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Phase</TableHead>
            <TableHead>Form</TableHead>
            <TableHead>Size</TableHead>
            <TableHead>What changed from the phase before</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {phaseSummary(p, source).map((r, i) => (
            <TableRow key={r.phase}>
              <TableCell className="whitespace-nowrap">{i + 1}. {r.phase} <Tip k={PHASE_HELP[r.phase]!} id={`ph-${i}`} /></TableCell>
              <TableCell>{r.form}</TableCell>
              <TableCell className="whitespace-nowrap tabular-nums">{r.unit === "bytes" ? bytes(r.size) : `${r.size.toLocaleString("en-US")} characters`}</TableCell>
              <TableCell className="min-w-64 whitespace-normal">{r.change}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function Uplc({ detail, instance }: { detail: ContractDetail; instance: InstanceDetail | null }) {
  const base = detail.pipeline;
  return (
    <Panel aria-labelledby="p-uplc">
      <Heading id="p-uplc">2. UPLC <Tip k="phase_uplc" id="p-uplc-tip" /></Heading>
      <Note>Decoded from the committed script by plutigo, so it is exactly what the chain runs; no compiler is needed to see it. Source lines are not mapped to UPLC terms.</Note>
      <Subheading>{instance ? "Before parameter application, as the compiler wrote it" : detail.params?.length ? "Compiler output, parameters not yet applied" : "Compiler output"}</Subheading>
      <CodeBlock text={base.uplc} label="UPLC before parameter application" />
      {instance && (
        <>
          <Subheading>After applying the parameters, {instance.name}</Subheading>
          <Note>Applying wraps the program in an application of each parameter, here the integer nonce as Data: the term grows by one argument per parameter and nothing inside it changes.</Note>
          <CodeBlock text={instance.pipeline.uplc} label={`UPLC of ${instance.name}`} />
        </>
      )}
    </Panel>
  );
}

function Flat({ detail, instance }: { detail: ContractDetail; instance: InstanceDetail | null }) {
  const p = instance?.pipeline ?? detail.pipeline;
  return (
    <Panel aria-labelledby="p-flat">
      <Heading id="p-flat">3. Flat bytes <Tip k="phase_flat" id="p-flat-tip" /></Heading>
      <Facts rows={[
        ["Size", bytes(p.flat_size)],
        ...(instance ? [["Before parameter application", bytes(detail.pipeline.flat_size)] as [string, string]] : []),
      ]} />
      <Hex hex={p.flat} />
    </Panel>
  );
}

function OnChain({ detail, instance }: { detail: ContractDetail; instance: InstanceDetail | null }) {
  const p = instance?.pipeline ?? detail.pipeline;
  const tag = p.tag.toString(16).padStart(2, "0");
  return (
    <Panel aria-labelledby="p-chain">
      <Heading id="p-chain">4. On-chain bytes <Tip k="phase_onchain" id="p-chain-tip" /></Heading>
      <Facts rows={[
        ["Size", bytes(p.wrapped_size)],
        ["CBOR header", bytes(p.wrapped_size - p.flat_size)],
      ]} />
      <Hex hex={p.wrapped} head={(p.wrapped_size - p.flat_size) * 2} />
      <Subheading>Script hash <Tip k="script_hash_derivation" id="p-hash-tip" /></Subheading>
      <p className="[overflow-wrap:anywhere]">
        blake2b-224( <Mono>{tag}</Mono> language tag, PlutusV{p.tag} ‖ the {bytes(p.wrapped_size)} above ) = <Mono>{p.hash}</Mono>
      </p>
      {instance && <Note>Without the parameters the hash is <Mono>{detail.pipeline.hash}</Mono>: every instance has its own, and so its own address.</Note>}
    </Panel>
  );
}

/** ContractBody shows a validator through each phase of its compilation; instance is the validator applied to selected, if any. */
export function ContractBody({ detail, instance, selected }: { detail: ContractDetail; instance: InstanceDetail | null; selected: number | null }) {
  const shown = instance?.pipeline ?? detail.pipeline;
  const module = detail.sources[0]?.text ?? "";
  return (
    <>
      <Note>Compiled by {detail.toolchain}. {selected !== null ? `Showing instance ${instance?.name ?? `${detail.name}#${selected}`}, the validator applied to nonce ${selected}.` : detail.params?.length ? `Parameters (${detail.params.join(", ")}) are not applied; pick an instance to apply them.` : "This validator takes no parameters."}</Note>
      <Panel aria-labelledby="p-pipeline">
        <Heading id="p-pipeline">Pipeline</Heading>
        <Summary p={shown} source={module} />
        {instance && (
          <Note>Applying the parameters changed the flat program from {bytes(detail.pipeline.flat_size)} to {bytes(instance.pipeline.flat_size)} and the script hash.</Note>
        )}
      </Panel>
      <Panel aria-labelledby="p-source">
        <Heading id="p-source">1. Aiken source <Tip k="phase_source" id="p-source-tip" /></Heading>
        {detail.sources.map((s) => (
          <div key={s.path}>
            <Subheading><Mono>{s.path}</Mono></Subheading>
            <CodeBlock text={s.text} label={s.path} />
          </div>
        ))}
      </Panel>
      <Uplc detail={detail} instance={instance} />
      <Flat detail={detail} instance={instance} />
      <OnChain detail={detail} instance={instance} />
    </>
  );
}

function Instance({ name, detail, n }: { name: string; detail: ContractDetail; n: number }) {
  const { data, error } = usePoll<InstanceDetail>(`/api/contracts/${name}/instances/${n}`, 60_000);
  return (
    <>
      <ErrorNote error={error} />
      <ContractBody detail={detail} instance={data} selected={n} />
    </>
  );
}

function Picker({ detail, selected }: { detail: ContractDetail; selected: number | undefined }) {
  if (!detail.params?.length) return null;
  const listed = selected !== undefined && !detail.instances.includes(selected) ? [...detail.instances, selected] : detail.instances;
  return (
    <div className="mb-3 flex flex-col gap-1">
      <label htmlFor="c-instance" className="text-[0.85rem] text-muted-foreground">Instance</label>
      <NativeSelect
        id="c-instance"
        value={selected === undefined ? "" : String(selected)}
        className="min-w-48"
        onChange={(e) => navigate(e.currentTarget.value === "" ? `/contracts/${detail.name}` : `/contracts/${detail.name}/${e.currentTarget.value}`)}
      >
        <option value="">Compiler output, parameters not applied</option>
        {listed.map((n) => <option key={n} value={n}>{detail.name}#{n}</option>)}
      </NativeSelect>
      {detail.instances.length === 0 && <Muted>No contract of this validator is deployed yet.</Muted>}
    </div>
  );
}

/** ContractView is /contracts/{name} and /contracts/{name}/{n}. */
export function ContractView({ name, instance }: { name: string; instance?: number }) {
  const { data, error } = usePoll<ContractDetail>(`/api/contracts/${name}`, 30_000);
  return (
    <>
      <Title>{name}</Title>
      <Note><Link to="/contracts">All contracts</Link></Note>
      <Blurb text={VIEW_BLURB.contract!} />
      <ErrorNote error={error} />
      {data && <Picker detail={data} selected={instance} />}
      {data && (instance === undefined ? <ContractBody detail={data} instance={null} selected={null} /> : <Instance name={name} detail={data} n={instance} />)}
    </>
  );
}
