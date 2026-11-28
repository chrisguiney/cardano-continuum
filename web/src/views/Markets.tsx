import { useMemo, useState } from "react";
import { ada, count, simTime } from "../lib/format";
import { VIEW_BLURB } from "../lib/help";
import { usePoll } from "../lib/hooks";
import { bids, bounds, currencies, flowRows, indexed, linePath, splitShare, type Series } from "../lib/markets";
import type { MarketsView } from "../lib/types";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, ErrorNote, Heading, Muted, Note, Stat, Stats, TableFrame, Title } from "./common";
import { Blurb } from "./fields";

const W = 720;
const H = 200;
/** SWATCH are the chart tokens, cycled when there are more lines than tokens. */
const SWATCH = ["var(--chart-a)", "var(--chart-b)", "var(--chart-c)"];

/** Chart draws lines of series in one box, with the drawdown window shaded when given. */
function Chart({ series, label, shade }: { series: Series[]; label: string; shade?: [number, number] }) {
  const box = bounds(series);
  if (!box) return <Note>No quotes yet.</Note>;
  const sx = (x: number) => (box.x1 === box.x0 ? 0 : ((x - box.x0) / (box.x1 - box.x0)) * W);
  return (
    <figure className="my-2">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} className="h-auto w-full rounded-md border bg-card">
        {shade && shade[1] > shade[0] && (
          <rect x={sx(shade[0])} y={0} width={Math.max(1, sx(shade[1]) - sx(shade[0]))} height={H} fill="var(--muted)" />
        )}
        {series.map((s, i) => (
          <path key={s.label} d={linePath(s.points, box, W, H)} fill="none" stroke={SWATCH[i % SWATCH.length]} strokeWidth={1.5}
            strokeDasharray={i >= SWATCH.length ? "4 3" : undefined} />
        ))}
      </svg>
      <figcaption className="mt-1 flex flex-wrap gap-3 text-xs text-muted-foreground">
        {series.map((s, i) => (
          <span key={s.label} className="inline-flex items-center gap-1">
            <span className="inline-block h-0.5 w-4" style={{ background: SWATCH[i % SWATCH.length] }} />
            {s.label}
          </span>
        ))}
        <span>
          {box.y0.toPrecision(3)} to {box.y1.toPrecision(3)}, {simTime(box.x0)} to {simTime(box.x1)}
        </span>
      </figcaption>
    </figure>
  );
}

export function Markets() {
  const { data, error } = usePoll<MarketsView>("/api/markets", 5000);
  const quotes = useMemo(() => data?.quotes ?? [], [data]);
  const codes = useMemo(() => currencies(quotes), [quotes]);
  const [cur, setCur] = useState("");
  const shown = cur || codes[0] || "";
  const d = data?.drawdown;
  const window: [number, number] | undefined = d && d.depth < 0 ? [d.peak_at, d.trough_at] : undefined;
  const flows = flowRows(data?.flows ?? []);
  const share = d ? splitShare(d.hodlers, d.sellers) : null;
  const last = quotes[quotes.length - 1];
  return (
    <>
      <Title>Markets</Title>
      <Blurb text={VIEW_BLURB.markets!} />
      <ErrorNote error={error} />
      {data && quotes.length === 0 && <Note>This run has no market: its profile file has no markets section.</Note>}
      <Stats>
        <Stat label="ADA/USD" value={last ? last.ada.toFixed(4) : "–"} sub={last ? `at ${simTime(last.at)}` : undefined} />
        <Stat label="Largest drawdown" value={d && d.depth < 0 ? `${(100 * d.depth).toFixed(1)}%` : "none"}
          sub={window ? `${simTime(window[0])} to ${simTime(window[1])}` : undefined} />
        <Stat label="Held in it" value={d ? count(d.hodlers) : "–"} sub="wallets under water that never sold" />
        <Stat label="Sold in it" value={d ? count(d.sellers) : "–"} sub={share === null ? undefined : `${(100 * share).toFixed(0)}% of those under water`} />
      </Stats>

      <Heading>ADA in each currency</Heading>
      <Muted>Mid rate rebased to 100 at the first quote; the shaded span is the largest ADA/USD drawdown.</Muted>
      <Chart series={indexed(quotes, codes)} label="ADA mid rate per currency, rebased to 100" shade={window} />

      <Heading>Exchange bids</Heading>
      <label className="text-sm">
        Currency{" "}
        <select className="rounded-md border bg-background px-2 py-1" value={shown} onChange={(e) => setCur(e.target.value)}>
          {codes.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </label>
      <Chart series={bids(quotes, shown)} label={`Each exchange's bid for ADA in ${shown}`} />

      <Heading>Flows by exchange</Heading>
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Exchange</TableHead><TableHead className="text-right">Buys</TableHead><TableHead className="text-right">ADA bought</TableHead>
              <TableHead className="text-right">Sells</TableHead><TableHead className="text-right">ADA sold</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {flows.map((f) => (
              <TableRow key={f.exchange}>
                <TableCell>{f.exchange}</TableCell>
                <TableCell className="text-right tabular-nums">{count(f.buys)}</TableCell>
                <TableCell className="text-right tabular-nums">{ada(f.bought)}</TableCell>
                <TableCell className="text-right tabular-nums">{count(f.sells)}</TableCell>
                <TableCell className="text-right tabular-nums">{ada(f.sold)}</TableCell>
              </TableRow>
            ))}
            {flows.length === 0 && <EmptyRow cols={5}>No trade has settled yet.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>

      <Heading>Births by currency</Heading>
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow><TableHead>Currency</TableHead><TableHead className="text-right">Wallets born</TableHead></TableRow>
          </TableHeader>
          <TableBody>
            {Object.entries(data?.births ?? {}).sort().map(([c, n]) => (
              <TableRow key={c}><TableCell>{c}</TableCell><TableCell className="text-right tabular-nums">{count(n)}</TableCell></TableRow>
            ))}
            {Object.keys(data?.births ?? {}).length === 0 && <EmptyRow cols={2}>No wallet born yet.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>
    </>
  );
}
