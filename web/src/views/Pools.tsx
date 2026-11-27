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
import { ErrorNote, Link } from "./common";
import { VoteTable } from "./Governance";

const W = 640;
const H = 420;
// Shapes vary by implementation so colour is not the only cue.
const SHAPES = ["circle", "square", "diamond", "triangle"] as const;

function Shape({ kind, x, y, fill, fork }: { kind: (typeof SHAPES)[number]; x: number; y: number; fill: string; fork: boolean }) {
  const stroke = fork ? "var(--bad)" : "var(--surface)";
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
  if (v.topology.nodes.length === 0) return <p className="empty">No pools in the topology yet.</p>;
  return (
    <>
      <svg viewBox={`0 0 ${W} ${H}`} className="graph" role="img" aria-label={`Pool topology, ${v.topology.mode} mode, ${v.topology.nodes.length} pools`}>
        {v.topology.edges.map((e) => {
          const a = pos.get(e.from);
          const b = pos.get(e.to);
          return a && b ? <line key={`${e.from}-${e.to}`} x1={a.x} y1={a.y} x2={b.x} y2={b.y} className="edge" /> : null;
        })}
        {v.topology.nodes.map((n) => {
          const p = pos.get(n.id);
          if (!p) return null;
          const f = forked.has(n.id);
          return (
            <g key={n.id}>
              <title>{`${n.id}: ${n.impl}, ${n.tier}, ${n.phase}${f ? ", fork" : ""}`}</title>
              <Shape kind={SHAPES[impls.indexOf(n.impl) % SHAPES.length]!} x={p.x} y={p.y} fill={colors.get(n.impl) ?? "#888"} fork={f} />
              <text x={p.x} y={p.y + 28} className="node-label">{n.id}</text>
            </g>
          );
        })}
      </svg>
      <ul className="legend">
        {impls.map((i) => (
          <li key={i}><span className="swatch" style={{ background: colors.get(i) }} />{i}</li>
        ))}
        <li><span className="swatch fork-swatch" />fork</li>
        <li className="muted">mode: {v.topology.mode}</li>
      </ul>
    </>
  );
}

const POOL_DEFAULTS: PoolFilterState = { impl: "", tier: "", phase: "" };
/** SPOVotes lists each pool actor's recent votes, from the engine's chain view. */
function SPOVotes() {
  const { data: g, error } = usePoll<GovernanceView>("/api/governance", 5000);
  return (
    <section className="card">
      <h2>Recent SPO votes</h2>
      <ErrorNote error={error} />
      {g && g.pools.length === 0 && <p className="empty">No pool has voted yet.</p>}
      {g?.pools.map((p) => (
        <div key={p.actor}>
          <h3><Link to={`/actors/${p.actor}`}>pool actor {p.actor}</Link>{p.profile && <span className="muted"> {p.profile}</span>}</h3>
          <VoteTable votes={p.votes} empty="No votes." />
        </div>
      ))}
    </section>
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
      <h1>Pools</h1>
      <Blurb text={VIEW_BLURB.pools!} />
      <form className="filters" aria-label="Filter the pool table" onSubmit={(e) => e.preventDefault()}>
        <PoolFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
        <span className="muted">Filters apply to the table below.</span>
      </form>
      <ErrorNote error={error} />
      {!v && !error && <p className="muted">Loading…</p>}
      {v && (
        <>
          {(impls.length > 0 || v.forks.length > 0) && (
            <div className="banner bad" role="status">
              {v.forks.length > 0
                ? v.forks.map((f) => <p key={`${f.reason}-${f.height ?? f.slot}`}>Fork: {forkLine(f)}.</p>)
                : <p>Fork: {impls.join(", ")} disagree on the tip.</p>}
            </div>
          )}
          <section className="card">
            <h2>Tip per implementation</h2>
            <ul className="tips">
              {v.tips.map((t) => (
                <li key={t.impl}><strong>{t.impl}</strong> {t.height ? `block ${count(t.height)}, ` : ""}slot {count(t.slot)} <code>{short(t.hash, 12)}</code> ({t.pools} pools)</li>
              ))}
              {v.tips.length === 0 && <li className="muted">No tips reported yet.</li>}
            </ul>
          </section>
          <section className="card">
            <h2>Topology</h2>
            <Topology v={v} forked={forked} />
          </section>
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>Pool</th><th>Impl</th><th>Tier</th><th>Phase</th><th>Stake</th><th>Blocks (made / expected)</th><th>Tip</th><th>Fork</th></tr>
              </thead>
              <tbody>
                {v.pools.filter((p) => (!filt.impl || p.impl === filt.impl) && (!filt.tier || p.tier === filt.tier) && (!filt.phase || p.phase === filt.phase)).map((p) => (
                  <tr key={p.id} className={p.dark ? "dim" : undefined}>
                    <td>{p.id}</td>
                    <td>{p.impl}</td>
                    <td>{p.tier}</td>
                    <td>{p.phase}{p.dark ? " (dark)" : ""}</td>
                    <td className="num">{p.stake === null ? <span title="the engine does not read pool stake from the chain view">–</span> : count(p.stake)}</td>
                    <td className="num">
                      {p.blocks_made === null ? <span title="blocks are not attributed to pools by issuer key yet">–</span> : count(p.blocks_made)} / {p.blocks_expected === null ? "–" : p.blocks_expected.toFixed(1)}
                    </td>
                    <td>{p.tip.height ? `${count(p.tip.height)} / ` : ""}{count(p.tip.slot)} <code>{short(p.tip.hash, 8)}</code></td>
                    <td>{forked.has(p.id) ? <span className="tag bad">fork</span> : ""}</td>
                  </tr>
                ))}
                {v.pools.length === 0 && <tr><td colSpan={8} className="empty">No pools.</td></tr>}
              </tbody>
            </table>
          </div>
          <SPOVotes />
        </>
      )}
    </>
  );
}
