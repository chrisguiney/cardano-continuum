import { useMemo } from "preact/hooks";
import { implColors } from "../lib/colors";
import { forkedImpls, forkedPools } from "../lib/forks";
import { count, short } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { layout } from "../lib/layout";
import type { PoolsView } from "../lib/types";
import { ErrorNote } from "./common";

const W = 640;
const H = 420;
// Shapes vary by implementation so colour is not the only cue.
const SHAPES = ["circle", "square", "diamond", "triangle"] as const;

function Shape({ kind, x, y, fill, fork }: { kind: (typeof SHAPES)[number]; x: number; y: number; fill: string; fork: boolean }) {
  const stroke = fork ? "var(--bad)" : "var(--surface)";
  const common = { fill, stroke, "stroke-width": fork ? 4 : 2 };
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
  if (v.topology.nodes.length === 0) return <p class="empty">No pools in the topology yet.</p>;
  return (
    <>
      <svg viewBox={`0 0 ${W} ${H}`} class="graph" role="img" aria-label={`Pool topology, ${v.topology.mode} mode, ${v.topology.nodes.length} pools`}>
        {v.topology.edges.map((e) => {
          const a = pos.get(e.from);
          const b = pos.get(e.to);
          return a && b ? <line key={`${e.from}-${e.to}`} x1={a.x} y1={a.y} x2={b.x} y2={b.y} class="edge" /> : null;
        })}
        {v.topology.nodes.map((n) => {
          const p = pos.get(n.id);
          if (!p) return null;
          const f = forked.has(n.id);
          return (
            <g key={n.id}>
              <title>{`${n.id}: ${n.impl}, ${n.tier}, ${n.phase}${f ? ", fork" : ""}`}</title>
              <Shape kind={SHAPES[impls.indexOf(n.impl) % SHAPES.length]!} x={p.x} y={p.y} fill={colors.get(n.impl) ?? "#888"} fork={f} />
              <text x={p.x} y={p.y + 28} class="node-label">{n.id}</text>
            </g>
          );
        })}
      </svg>
      <ul class="legend">
        {impls.map((i) => (
          <li key={i}><span class="swatch" style={{ background: colors.get(i) }} />{i}</li>
        ))}
        <li><span class="swatch fork-swatch" />fork</li>
        <li class="muted">mode: {v.topology.mode}</li>
      </ul>
    </>
  );
}

export function Pools() {
  const { data: v, error } = usePoll<PoolsView>("/api/pools", 3000);
  const forked = useMemo(() => (v ? forkedPools(v) : new Set<string>()), [v]);
  const impls = v ? forkedImpls(v.tips) : [];
  return (
    <>
      <h1>Pools</h1>
      <ErrorNote error={error} />
      {!v && !error && <p class="muted">Loading…</p>}
      {v && (
        <>
          {(impls.length > 0 || v.forks.length > 0) && (
            <p class="banner bad" role="status">
              Fork: {impls.length > 0 ? `${impls.join(", ")} disagree on the tip` : "pools disagree on a block hash"}.
            </p>
          )}
          <section class="card">
            <h2>Tip per implementation</h2>
            <ul class="tips">
              {v.tips.map((t) => (
                <li key={t.impl}><strong>{t.impl}</strong> slot {count(t.slot)} <code>{short(t.hash, 12)}</code> ({t.pools} pools)</li>
              ))}
              {v.tips.length === 0 && <li class="muted">No tips reported yet.</li>}
            </ul>
          </section>
          <section class="card">
            <h2>Topology</h2>
            <Topology v={v} forked={forked} />
          </section>
          <div class="table-wrap">
            <table>
              <thead>
                <tr><th>Pool</th><th>Impl</th><th>Tier</th><th>Phase</th><th>Stake</th><th>Blocks (made / expected)</th><th>Tip</th><th>Fork</th></tr>
              </thead>
              <tbody>
                {v.pools.map((p) => (
                  <tr key={p.id} class={p.dark ? "dim" : undefined}>
                    <td>{p.id}</td>
                    <td>{p.impl}</td>
                    <td>{p.tier}</td>
                    <td>{p.phase}{p.dark ? " (dark)" : ""}</td>
                    <td class="num">{p.stake === null ? "–" : count(p.stake)}</td>
                    <td class="num">
                      {p.blocks_made === null ? "–" : count(p.blocks_made)} / {p.blocks_expected === null ? "–" : p.blocks_expected.toFixed(1)}
                    </td>
                    <td>{count(p.tip.slot)} <code>{short(p.tip.hash, 8)}</code></td>
                    <td>{forked.has(p.id) ? <span class="tag bad">fork</span> : ""}</td>
                  </tr>
                ))}
                {v.pools.length === 0 && <tr><td colSpan={8} class="empty">No pools.</td></tr>}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}
