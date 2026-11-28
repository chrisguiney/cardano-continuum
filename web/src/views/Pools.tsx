import { useEffect, useMemo, useState } from "react";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { implColors } from "../lib/colors";
import { forkLine, forkedImpls, forkedPools } from "../lib/forks";
import { count, short } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { layout } from "../lib/layout";
import { Blurb } from "./fields";
import { PoolFilterBar, type PoolFilterState } from "./filterbars";
import type { GovernanceView, PoolsView } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "../lib/utils";
import { Banner, EmptyRow, ErrorNote, Filters, Heading, Link, Muted, Note, Panel, Subheading, TableFrame, Title } from "./common";
import { VoteTable } from "./Governance";

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

function Topology({ v, forked }: { v: PoolsView; forked: Set<string> }) {
  const impls = [...new Set(v.topology.nodes.map((n) => n.impl))].sort();
  const colors = implColors(impls);
  const pos = useMemo(() => layout(v.topology.nodes, v.topology.edges, W, H), [JSON.stringify(v.topology.nodes.map((n) => n.id)), JSON.stringify(v.topology.edges)]);
  if (v.topology.nodes.length === 0) return <Note className="p-4 text-center">No pools in the topology yet.</Note>;
  return (
    <>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full max-w-[720px] rounded-lg bg-background" role="img" aria-label={`Pool topology, ${v.topology.mode} mode, ${v.topology.nodes.length} pools`}>
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

const POOL_DEFAULTS: PoolFilterState = { impl: "", tier: "", phase: "" };
/** SPOVotes lists each pool actor's recent votes, from the engine's chain view. */
function SPOVotes() {
  const { data: g, error } = usePoll<GovernanceView>("/api/governance", 5000);
  return (
    <Panel>
      <Heading>Recent SPO votes</Heading>
      <ErrorNote error={error} />
      {g && g.pools.length === 0 && <Note className="p-4 text-center">No pool has voted yet.</Note>}
      {g?.pools.map((p) => (
        <div key={p.actor}>
          <Subheading><Link to={`/actors/${p.actor}`}>pool actor {p.actor}</Link>{p.profile && <Muted className="font-normal"> {p.profile}</Muted>}</Subheading>
          <VoteTable votes={p.votes} empty="No votes." />
        </div>
      ))}
    </Panel>
  );
}

export function Pools() {
  const [filt, setFilt] = useState<PoolFilterState>(() => parseQuery(location.search, POOL_DEFAULTS));
  const enums = useEnums();
  useEffect(() => writeQuery(buildQuery({ ...filt }, POOL_DEFAULTS)), [filt]);
  const { data: v, error } = usePoll<PoolsView>("/api/pools", 3000);
  const forked = useMemo(() => (v ? forkedPools(v) : new Set<string>()), [v]);
  const impls = v ? forkedImpls(v.tips) : [];
  return (
    <>
      <Title>Pools</Title>
      <Blurb text={VIEW_BLURB.pools!} />
      <Filters aria-label="Filter the pool table">
        <PoolFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
        <Muted className="pb-2">Filters apply to the table below.</Muted>
      </Filters>
      <ErrorNote error={error} />
      {!v && !error && <Note>Loading…</Note>}
      {v && (
        <>
          {(impls.length > 0 || v.forks.length > 0) && (
            <Banner tone="bad" role="status">
              {v.forks.length > 0
                ? v.forks.map((f) => <p key={`${f.reason}-${f.height ?? f.slot}`}>Fork: {forkLine(f)}.</p>)
                : <p>Fork: {impls.join(", ")} disagree on the tip.</p>}
            </Banner>
          )}
          <Panel>
            <Heading>Tip per implementation</Heading>
            <ul className="list-disc pl-5">
              {v.tips.map((t) => (
                <li key={t.impl}><strong>{t.impl}</strong> {t.height ? `block ${count(t.height)}, ` : ""}slot {count(t.slot)} <code className="text-[0.85em]">{short(t.hash, 12)}</code> ({t.pools} pools)</li>
              ))}
              {v.tips.length === 0 && <li className="text-muted-foreground">No tips reported yet.</li>}
            </ul>
          </Panel>
          <Panel>
            <Heading>Topology</Heading>
            <Topology v={v} forked={forked} />
          </Panel>
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Pool</TableHead><TableHead>Impl</TableHead><TableHead>Tier</TableHead><TableHead>Phase</TableHead><TableHead className="text-right">Stake</TableHead>
                  <TableHead className="text-right">Blocks (made / expected)</TableHead><TableHead>Tip</TableHead><TableHead>Fork</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {v.pools.filter((p) => (!filt.impl || p.impl === filt.impl) && (!filt.tier || p.tier === filt.tier) && (!filt.phase || p.phase === filt.phase)).map((p) => (
                  <TableRow key={p.id} className={cn(p.dark && "opacity-60")}>
                    <TableCell>{p.id}</TableCell>
                    <TableCell>{p.impl}</TableCell>
                    <TableCell>{p.tier}</TableCell>
                    <TableCell>{p.phase}{p.dark ? " (dark)" : ""}</TableCell>
                    <TableCell className="text-right tabular-nums">{p.stake === null ? <span title="the engine does not read pool stake from the chain view">–</span> : count(p.stake)}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {p.blocks_made === null ? <span title="blocks are not attributed to pools by issuer key yet">–</span> : count(p.blocks_made)} / {p.blocks_expected === null ? "–" : p.blocks_expected.toFixed(1)}
                    </TableCell>
                    <TableCell>{p.tip.height ? `${count(p.tip.height)} / ` : ""}{count(p.tip.slot)} <code className="text-[0.85em]">{short(p.tip.hash, 8)}</code></TableCell>
                    <TableCell>{forked.has(p.id) ? <Badge variant="bad">fork</Badge> : ""}</TableCell>
                  </TableRow>
                ))}
                {v.pools.length === 0 && <EmptyRow cols={8}>No pools.</EmptyRow>}
              </TableBody>
            </Table>
          </TableFrame>
          <SPOVotes />
        </>
      )}
    </>
  );
}
