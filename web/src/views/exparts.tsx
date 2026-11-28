import type { ReactNode } from "react";
import { addrLink, blockLink, nodeLink, poolLink, txLink, usage } from "../lib/explorer";
import type { Asset, Output, PoolRef } from "../lib/extypes";
import { short } from "../lib/format";
import { Button } from "@/components/ui/button";
import { TableCell } from "@/components/ui/table";
import { CopyButton, Facts as FactList, Heading, Link, Mono, Muted, Panel } from "./common";

/** HashLink is a shortened hash that links to its view and carries a copy button. */
export function HashLink({ value, to, n = 10, label }: { value: string; to?: string; n?: number; label: string }) {
  const text = <Mono title={value}>{short(value, n)}</Mono>;
  return (
    <span className="inline-flex items-center whitespace-nowrap">
      {to ? <Link to={to}>{text}</Link> : text}
      <CopyButton value={value} label={label} />
    </span>
  );
}

export const BlockHash = ({ value, n }: { value: string; n?: number }) => <HashLink value={value} to={blockLink(value)} n={n} label="block hash" />;
export const TxHash = ({ value, n }: { value: string; n?: number }) => <HashLink value={value} to={txLink(value)} n={n} label="transaction hash" />;
export const Addr = ({ value }: { value: string }) => <HashLink value={value} to={addrLink(value)} n={18} label="address" />;

/**
 * PoolChip names the stake pool that issued a block, linking the pool, and
 * when a node runs its keys that node by name and implementation, linking
 * the node.
 */
export function PoolChip({ issuer, pool }: { issuer: string; pool?: PoolRef }) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <Link to={poolLink(issuer)}><Mono title={issuer}>{short(issuer, pool ? 6 : 10)}</Mono></Link>
      {pool && <Link to={nodeLink(pool.name)}>{pool.name} <Muted>({pool.impl})</Muted></Link>}
    </span>
  );
}

/** UsageBar draws a used-of-limit meter with its percentage as text, so colour is never the only cue. */
export function UsageBar({ used, limit, title, name }: { used: number; limit: number; title: string; name: string }) {
  const u = usage(used, limit);
  return (
    <span className="flex items-center gap-1.5" title={title}>
      <span className="min-w-[4.2em] text-xs text-muted-foreground">{name}</span>
      <span className="inline-block h-2.5 w-16 overflow-hidden rounded-sm border bg-muted" role="img" aria-label={`${name}: ${u.text} of the block limit used`}><span className="block h-full bg-primary" style={{ width: `${u.pct}%` }} /></span>
      <span className="min-w-[3ch] text-xs">{u.text}</span>
    </span>
  );
}

export function Pager({ offset, limit, total, onPage }: { offset: number; limit: number; total: number; onPage: (offset: number) => void }) {
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(total, offset + limit);
  return (
    <nav className="my-4 flex items-center justify-center gap-4" aria-label="Pages">
      <Button type="button" variant="outline" onClick={() => onPage(Math.max(0, offset - limit))} disabled={offset === 0}>Newer</Button>
      <span role="status">{from.toLocaleString("en-US")} to {to.toLocaleString("en-US")} of {total.toLocaleString("en-US")}</span>
      <Button type="button" variant="outline" onClick={() => onPage(offset + limit)} disabled={offset + limit >= total}>Older</Button>
    </nav>
  );
}

export function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <Panel aria-labelledby={id}>
      <Heading id={id}>{title}</Heading>
      {children}
    </Panel>
  );
}

/** Facts is a block's or a transaction's fields, named in bold. */
export function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return <FactList rows={rows} />;
}

/** Assets lists native tokens by policy and name; the full policy id is in the tooltip. */
export function Assets({ items }: { items?: Asset[] }) {
  if (!items || items.length === 0) return <Muted>none</Muted>;
  return (
    <ul>
      {items.map((a) => (
        <li key={a.policy + a.name}>
          <span title={`policy ${a.policy}, name ${a.name || "(empty)"}`}>{a.quantity} {a.name_text ?? (a.name ? short(a.name, 8) : "(unnamed)")}</span>
          <Mono className="text-muted-foreground"> {short(a.policy, 8)}</Mono>
        </li>
      ))}
    </ul>
  );
}

/** OutputCells is the shared address, value, tokens, datum and script columns of an output row. */
export function OutputCells({ o, lovelace }: { o: Output; lovelace: string }) {
  return (
    <>
      <TableCell><Addr value={o.address} /></TableCell>
      <TableCell>{lovelace}</TableCell>
      <TableCell><Assets items={o.assets} /></TableCell>
      <TableCell>
        {o.datum ? (
          <span title={o.datum.hex ?? o.datum.hash}>{o.datum.kind === "inline" ? "inline " : "hash "}<Mono>{short(o.datum.hash, 10)}</Mono></span>
        ) : (
          <Muted>none</Muted>
        )}
      </TableCell>
      <TableCell>{o.script_ref ? <span title={o.script_ref.hash}>{o.script_ref.language} <Mono>{short(o.script_ref.hash, 10)}</Mono></span> : <Muted>none</Muted>}</TableCell>
    </>
  );
}
