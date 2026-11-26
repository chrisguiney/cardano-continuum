export interface LNode {
  id: string;
}
export interface LEdge {
  from: string;
  to: string;
}
export interface Pos {
  x: number;
  y: number;
}

const ITERATIONS = 200;
const PAD = 34;
// Repulsion is weaker than the textbook k^2/d and a weak pull toward the
// centre keeps unconnected nodes off the walls.
const REPEL = 0.25;
const GRAVITY = 0.05;

/**
 * layout places nodes with a small force simulation: every pair repels,
 * every edge pulls toward a rest length, and positions are clamped to the
 * box. It starts on a circle in input order and uses no randomness, so the
 * same graph always draws the same.
 */
export function layout(nodes: LNode[], edges: LEdge[], w: number, h: number): Map<string, Pos> {
  const out = new Map<string, Pos>();
  if (nodes.length === 0) return out;
  if (nodes.length === 1) return out.set(nodes[0]!.id, { x: w / 2, y: h / 2 });
  const cx = w / 2;
  const cy = h / 2;
  const r = Math.min(w, h) / 2 - PAD;
  const pts = nodes.map((n, i) => {
    const a = (2 * Math.PI * i) / nodes.length;
    return { id: n.id, x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
  });
  const index = new Map(pts.map((p, i) => [p.id, i]));
  const links = edges.flatMap((e) => {
    const a = index.get(e.from);
    const b = index.get(e.to);
    return a === undefined || b === undefined || a === b ? [] : [[a, b] as const];
  });
  const rest = Math.sqrt((w * h) / nodes.length) * 0.6;
  for (let it = 0; it < ITERATIONS; it++) {
    const cool = 1 - it / ITERATIONS;
    const fx = new Array<number>(pts.length).fill(0);
    const fy = new Array<number>(pts.length).fill(0);
    for (let i = 0; i < pts.length; i++) {
      for (let j = i + 1; j < pts.length; j++) {
        const dx = pts[i]!.x - pts[j]!.x || 0.01 * (i - j);
        const dy = pts[i]!.y - pts[j]!.y || 0.01;
        const d2 = Math.max(dx * dx + dy * dy, 1);
        const f = (REPEL * rest * rest) / d2;
        fx[i]! += dx * f;
        fy[i]! += dy * f;
        fx[j]! -= dx * f;
        fy[j]! -= dy * f;
      }
    }
    for (const [a, b] of links) {
      const dx = pts[b]!.x - pts[a]!.x;
      const dy = pts[b]!.y - pts[a]!.y;
      const d = Math.max(Math.hypot(dx, dy), 1);
      const f = (d - rest) / d;
      fx[a]! += dx * f * 0.5;
      fy[a]! += dy * f * 0.5;
      fx[b]! -= dx * f * 0.5;
      fy[b]! -= dy * f * 0.5;
    }
    for (let i = 0; i < pts.length; i++) {
      const p = pts[i]!;
      fx[i]! += (cx - p.x) * GRAVITY;
      fy[i]! += (cy - p.y) * GRAVITY;
      const step = Math.min(Math.hypot(fx[i]!, fy[i]!), 20 * cool + 1);
      const mag = Math.hypot(fx[i]!, fy[i]!) || 1;
      p.x = Math.min(w - PAD, Math.max(PAD, p.x + (fx[i]! / mag) * step));
      p.y = Math.min(h - PAD, Math.max(PAD, p.y + (fy[i]! / mag) * step));
    }
  }
  for (const p of pts) out.set(p.id, { x: p.x, y: p.y });
  return out;
}
