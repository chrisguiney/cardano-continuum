import { useEffect, useMemo, useState } from "react";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { implColors } from "../lib/colors";
import { forkLine, forkedImpls, forkedNodes } from "../lib/forks";
import { count, short } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { layout } from "../lib/layout";
import { Blurb, HelpTip } from "./fields";
import { FIELD_HELP } from "../lib/help";
import { sortNodes, type NodeSortKey } from "../lib/nodesort";
import { ArrowDownIcon, ArrowUpIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { NodeFilterBar, type NodeFilterState } from "./filterbars";
import type { NodeRow, NodesView, RollbackRow } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "../lib/utils";
import { nodeLink, poolLink } from "../lib/explorer";
import { Banner, EmptyRow, ErrorNote, Facts, Filters, Heading, Link, Mono, Muted, Note, Panel, TableFrame, Title } from "./common";

const W = 640;
const H = 420;
// Shapes vary by implementation so colour is not the only cue.
const SHAPES = ["circle", "square", "diamond", "triangle"] as const;

function Shape({ kind, x, y, fill, fork }: { kind: (typeof SHAPES)[number]; x: number; y: number; fill: string; fork: boolean }) {
  const stroke = fork ? "var(--destructive)" : "var(--card)";
  const common = { fill, stroke, strokeWidth: fork ? 4 : 2 };
  const r = 11;
  if (kind === "square") return <rect x={x - r} y={y - r} width={2 * r} height={2 * r} {...common} />;
  if (kind === "diamond") return <polygon points={`${x},${y - r - 3} ${x + r + 3},${y} ${x},${y + r + 3} ${x - r - 3},${y}`} {...common} />;
  if (kind === "triangle") return <polygon points={`${x},${y - r - 2} ${x + r + 2},${y + r} ${x - r - 2},${y + r}`} {...common} />;
  return <circle cx={x} cy={y} r={r} {...common} />;
}

function Topology({ v, forked }: { v: NodesView; forked: Set<string> }) {
  const impls = [...new Set(v.topology.nodes.map((n) => n.impl))].sort();
  const colors = implColors(impls);
  const pos = useMemo(() => layout(v.topology.nodes, v.topology.edges, W, H), [JSON.stringify(v.topology.nodes.map((n) => n.id)), JSON.stringify(v.topology.edges)]);
  if (v.topology.nodes.length === 0) return <Note className="p-4 text-center">No nodes in the topology yet.</Note>;
  return (
    <>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full max-w-[720px] rounded-lg bg-background" role="img" aria-label={`Node topology, ${v.topology.mode} mode, ${v.topology.nodes.length} nodes`}>
        {v.topology.edges.map((e) => {
          const a = pos.get(e.from);
          const b = pos.get(e.to);
          return a && b ? <line key={`${e.from}-${e.to}`} x1={a.x} y1={a.y} x2={b.x} y2={b.y} className="stroke-muted-foreground opacity-70" strokeWidth={1.5} /> : null;
        })}
        {v.topology.nodes.map((n) => {
          const p = pos.get(n.id);
          if (!p) return null;
          const f = forked.has(n.id);
          return (
            <g key={n.id}>
              <title>{`${n.id}: ${n.impl}, ${n.tier}, ${n.phase}${f ? ", fork" : ""}`}</title>
              <Shape kind={SHAPES[impls.indexOf(n.impl) % SHAPES.length]!} x={p.x} y={p.y} fill={colors.get(n.impl) ?? "#888"} fork={f} />
              <text x={p.x} y={p.y + 28} className="fill-foreground text-[11px]" textAnchor="middle">{n.id}</text>
            </g>
          );
        })}
      </svg>
      <ul className="mt-2 flex flex-wrap gap-4">
        {impls.map((i) => (
          <li key={i} className="flex items-center gap-1.5"><span className="inline-block size-3.5 rounded-sm" style={{ background: colors.get(i) }} />{i}</li>
        ))}
        <li className="flex items-center gap-1.5"><span className="inline-block size-3.5 rounded-sm border-[3px] border-destructive bg-card" />fork</li>
        <li className="text-muted-foreground">mode: {v.topology.mode}</li>
      </ul>
    </>
  );
}

const NODE_DEFAULTS: NodeFilterState = { impl: "", tier: "", phase: "" };

/** StakePoolCell links the stake pool whose keys a node runs, or says why there is none. */
export function StakePoolCell({ n }: { n: NodeRow }) {
  if (n.stake_pool) return <Link to={poolLink(n.stake_pool)}><Mono title={n.stake_pool}>{short(n.stake_pool, 10)}</Mono></Link>;
  if (n.tier !== "forging") return <Muted title="only a forging node holds a stake pool's keys">no keys ({n.tier})</Muted>;
  return <Muted title="the pool is known once the node forges its first block">not forged yet</Muted>;
}

/** BlockCount is a node's forged or verified count, or says it is unknown. */
function BlockCount({ v }: { v: number | null }) {
  return v === null ? <Muted title="the node's tip height or the chain index is not known yet">unknown</Muted> : <>{count(v)}</>;
}

function depthText(r: RollbackRow): string {
  return `${r.beyond ? "at least " : ""}${count(r.blocks)} ${r.blocks === 1 ? "block" : "blocks"} (${count(r.slots)} slots)`;
}

/** DepthCell is a rollback's depth, or says there was none. */
function DepthCell({ r }: { r: RollbackRow | null }) {
  return r ? <>{depthText(r)}</> : <Muted>no rollback</Muted>;
}

function LastCell({ r }: { r: RollbackRow | null }) {
  return r ? <>slot {count(r.from.slot)}</> : <Muted>no rollback</Muted>;
}

function findingsText(n: NodeRow): string {
  if (n.findings === 0) return "none";
  return n.known_findings > 0 ? `${count(n.findings)} (${count(n.known_findings)} known)` : count(n.findings);
}

const COLUMNS: { label: string; key?: NodeSortKey; num?: boolean; help?: string }[] = [
  { label: "Node", key: "name" }, { label: "Impl", key: "impl" }, { label: "Tier", key: "tier" }, { label: "Phase", key: "phase" }, { label: "Stake pool" },
  { label: "Tip" }, { label: "Slot lag", key: "slot_lag", num: true },
  { label: "Forged", key: "forged", num: true, help: "forged" }, { label: "Verified", key: "verified", num: true, help: "verified" },
  { label: "Rollbacks", key: "rollbacks", num: true, help: "rollbacks" }, { label: "Deepest", key: "deepest", help: "deepest_rollback" },
  { label: "Last rollback", key: "last_rollback", help: "last_rollback" },
  { label: "Findings" }, { label: "Fork" },
];

/** NodeTable lists the nodes, sortable by column; each row links the node and the stake pool it runs. */
export function NodeTable({ rows, forked, total }: { rows: NodeRow[]; forked: Set<string>; total: number }) {
  const [sort, setSort] = useState<{ key: NodeSortKey; desc: boolean } | null>(null);
  const shown = sort ? sortNodes(rows, sort.key, sort.desc) : rows;
  const click = (key: NodeSortKey) => setSort((s) => (s?.key === key ? { key, desc: !s.desc } : { key, desc: !!COLUMNS.find((c) => c.key === key)?.num }));
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            {COLUMNS.map((c) => {
              const dir = !c.key || sort?.key !== c.key ? "none" : sort.desc ? "descending" : "ascending";
              return (
                <TableHead key={c.label} aria-sort={c.key ? dir : undefined} className={c.num ? "text-right" : undefined}>
                  {c.key ? (
                    <Button type="button" variant="ghost" size="xs" className="-ml-2 font-semibold" onClick={() => click(c.key!)}>
                      {c.label}
                      {dir === "descending" && <ArrowDownIcon aria-hidden="true" />}
                      {dir === "ascending" && <ArrowUpIcon aria-hidden="true" />}
                    </Button>
                  ) : c.label}
                  {c.help && <HelpTip id={`nh-${c.help}`} label={FIELD_HELP[c.help]!.label} text={FIELD_HELP[c.help]!.help} />}
                </TableHead>
              );
            })}
          </TableRow>
        </TableHeader>
        <TableBody>
          {shown.map((n) => (
            <TableRow key={n.name} className={cn(n.dark && "opacity-60")}>
              <TableCell><Link to={nodeLink(n.name)}>{n.name}</Link></TableCell>
              <TableCell>{n.impl}</TableCell>
              <TableCell>{n.tier}</TableCell>
              <TableCell>{n.phase}{n.dark ? " (dark)" : ""}</TableCell>
              <TableCell><StakePoolCell n={n} /></TableCell>
              <TableCell>{n.tip.height ? `${count(n.tip.height)} / ` : ""}{count(n.tip.slot)} <code className="text-[0.85em]">{short(n.tip.hash, 8)}</code></TableCell>
              <TableCell className="text-right tabular-nums">{count(n.slot_lag)}</TableCell>
              <TableCell className="text-right tabular-nums"><BlockCount v={n.forged} /></TableCell>
              <TableCell className="text-right tabular-nums"><BlockCount v={n.verified} /></TableCell>
              <TableCell className="text-right tabular-nums">{count(n.rollbacks)}</TableCell>
              <TableCell><DepthCell r={n.deepest_rollback} /></TableCell>
              <TableCell><LastCell r={n.last_rollback} /></TableCell>
              <TableCell>{findingsText(n)}</TableCell>
              <TableCell>{forked.has(n.name) ? <Badge variant="bad">fork</Badge> : ""}</TableCell>
            </TableRow>
          ))}
          {total === 0 && <EmptyRow cols={COLUMNS.length}>No nodes. A run without a cluster has only on-chain stake pools.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

/** NodeDetail is one node in full, with a link to the stake pool it runs. */
export function NodeDetail({ n, forked }: { n: NodeRow; forked: boolean }) {
  return (
    <>
    <Panel>
      <Heading>Node {n.name}</Heading>
      <Facts rows={[
        ["Implementation", n.impl],
        ["Tier", n.tier],
        ["Phase", `${n.phase}${n.dark ? " (dark)" : ""}`],
        ["Stake pool", <StakePoolCell n={n} />],
        ["Label (spec.poolId)", <Mono>{n.label}</Mono>],
        ["Tip", <>{n.tip.height ? `block ${count(n.tip.height)}, ` : ""}slot {count(n.tip.slot)} <Mono>{short(n.tip.hash, 12)}</Mono></>],
        ["Slot lag", count(n.slot_lag)],
        [<>Forged<HelpTip id="nd-forged" label={FIELD_HELP.forged!.label} text={FIELD_HELP.forged!.help} /></>, <BlockCount v={n.forged} />],
        [<>Verified<HelpTip id="nd-verified" label={FIELD_HELP.verified!.label} text={FIELD_HELP.verified!.help} /></>, <BlockCount v={n.verified} />],
        [<>Rollbacks<HelpTip id="nd-rollbacks" label={FIELD_HELP.rollbacks!.label} text={FIELD_HELP.rollbacks!.help} /></>, count(n.rollbacks)],
        [<>Deepest<HelpTip id="nd-deepest" label={FIELD_HELP.deepest_rollback!.label} text={FIELD_HELP.deepest_rollback!.help} /></>, <DepthCell r={n.deepest_rollback} />],
        [<>Last rollback<HelpTip id="nd-last-rollback" label={FIELD_HELP.last_rollback!.label} text={FIELD_HELP.last_rollback!.help} /></>, n.last_rollback ? `slot ${count(n.last_rollback.from.slot)}, ${depthText(n.last_rollback)}` : <Muted>no rollback</Muted>],
        ["Last own block", n.last_block ? <Link to={`/explorer/blocks/${n.last_block.hash}`}>{n.last_block.height ? `block ${count(n.last_block.height)}, ` : ""}slot {count(n.last_block.slot)}</Link> : <Muted>none seen</Muted>],
        ["Image", n.image ? <Mono>{n.image}</Mono> : <Muted>unknown</Muted>],
        ["Findings", <Link to="/findings">{findingsText(n)}</Link>],
        ["Fork", forked ? <Badge variant="bad">on a disagreeing branch</Badge> : "no"],
      ]} />
    </Panel>
    <RecentRollbacks rows={n.recent_rollbacks ?? []} />
    </>
  );
}

const pointText = (p: { slot: number; height?: number }) => `${p.height ? `block ${count(p.height)}, ` : ""}slot ${count(p.slot)}`;

/** RecentRollbacks lists a node's newest rollbacks: the tip it held, the point it went back to, and the depth. */
export function RecentRollbacks({ rows }: { rows: RollbackRow[] }) {
  return (
    <Panel>
      <Heading>Recent rollbacks</Heading>
      {rows.length === 0 ? <Muted>no rollbacks seen</Muted> : (
        <TableFrame>
          <Table>
            <TableHeader><TableRow><TableHead>From tip</TableHead><TableHead>To</TableHead><TableHead>Depth</TableHead></TableRow></TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={`${r.from.hash}-${r.to.hash}`}>
                  <TableCell>{pointText(r.from)}</TableCell>
                  <TableCell>{r.beyond ? `slot ${count(r.to.slot)}` : pointText(r.to)}</TableCell>
                  <TableCell>{depthText(r)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableFrame>
      )}
    </Panel>
  );
}

/** Nodes is the node processes view; with name it opens on that node. */
export function Nodes({ name = "" }: { name?: string }) {
  const [filt, setFilt] = useState<NodeFilterState>(() => parseQuery(location.search, NODE_DEFAULTS));
  const enums = useEnums();
  useEffect(() => {
    if (!name) writeQuery(buildQuery({ ...filt }, NODE_DEFAULTS));
  }, [filt, name]);
  const { data: v, error } = usePoll<NodesView>("/api/nodes", 3000);
  const forked = useMemo(() => (v ? forkedNodes(v) : new Set<string>()), [v]);
  const impls = v ? forkedImpls(v.tips) : [];
  const one = name ? v?.nodes.find((n) => n.name === name) : undefined;
  return (
    <>
      <Title>{name ? `Node ${name}` : "Nodes"}</Title>
      <Blurb text={VIEW_BLURB.nodes!} />
      <ErrorNote error={error} />
      {!v && !error && <Note>Loading…</Note>}
      {v && name && (one ? <NodeDetail n={one} forked={forked.has(one.name)} /> : <Note className="p-4 text-center">No node named {name}. <Link to="/nodes">All nodes</Link></Note>)}
      {v && name && <Note><Link to="/nodes">All nodes</Link></Note>}
      {v && !name && (
        <>
          {(impls.length > 0 || v.forks.length > 0) && (
            <Banner tone="bad" role="status">
              {v.forks.length > 0
                ? v.forks.map((f) => <p key={`${f.reason}-${f.height ?? f.slot}`}>Fork: {forkLine(f)}.</p>)
                : <p>Fork: {impls.join(", ")} disagree on the tip.</p>}
            </Banner>
          )}
          <Filters aria-label="Filter the node table">
            <NodeFilterBar f={filt} enums={enums} onChange={(k, val) => setFilt((f) => ({ ...f, [k]: val }))} />
            <Muted className="pb-2">Filters apply to the table below.</Muted>
          </Filters>
          <NodeTable rows={v.nodes.filter((n) => (!filt.impl || n.impl === filt.impl) && (!filt.tier || n.tier === filt.tier) && (!filt.phase || n.phase === filt.phase))} forked={forked} total={v.nodes.length} />
          <Panel>
            <Heading>Tip per implementation</Heading>
            <ul className="list-disc pl-5">
              {v.tips.map((t) => (
                <li key={t.impl}><strong>{t.impl}</strong> {t.height ? `block ${count(t.height)}, ` : ""}slot {count(t.slot)} <code className="text-[0.85em]">{short(t.hash, 12)}</code> ({t.nodes} nodes)</li>
              ))}
              {v.tips.length === 0 && <li className="text-muted-foreground">No tips reported yet.</li>}
            </ul>
          </Panel>
          <Panel>
            <Heading>Topology</Heading>
            <Topology v={v} forked={forked} />
          </Panel>
        </>
      )}
    </>
  );
}
