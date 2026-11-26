import type { ComponentChildren } from "preact";
import { addrLink, blockLink, poolLink, txLink, usage } from "../lib/explorer";
import type { Asset, Output, PoolRef } from "../lib/extypes";
import { short } from "../lib/format";
import { CopyButton, Link } from "./common";

/** HashLink is a shortened hash that links to its view and carries a copy button. */
export function HashLink({ value, to, n = 10, label }: { value: string; to?: string; n?: number; label: string }) {
  const text = <span class="mono" title={value}>{short(value, n)}</span>;
  return (
    <span class="hash">
      {to ? <Link to={to}>{text}</Link> : text}
      <CopyButton value={value} label={label} />
    </span>
  );
}

export const BlockHash = ({ value, n }: { value: string; n?: number }) => <HashLink value={value} to={blockLink(value)} n={n} label="block hash" />;
export const TxHash = ({ value, n }: { value: string; n?: number }) => <HashLink value={value} to={txLink(value)} n={n} label="transaction hash" />;
export const Addr = ({ value }: { value: string }) => <HashLink value={value} to={addrLink(value)} n={18} label="address" />;

/** PoolChip names the pool that issued a block: a continuum pool by name and implementation, else the id. */
export function PoolChip({ issuer, pool }: { issuer: string; pool?: PoolRef }) {
  return (
    <span class="hash">
      <Link to={poolLink(issuer)}>{pool ? <span title={issuer}>{pool.name} <span class="muted">({pool.impl})</span></span> : <span class="mono" title={issuer}>{short(issuer, 10)}</span>}</Link>
    </span>
  );
}

/** UsageBar draws a used-of-limit meter with its percentage as text, so colour is never the only cue. */
export function UsageBar({ used, limit, title, name }: { used: number; limit: number; title: string; name: string }) {
  const u = usage(used, limit);
  return (
    <span class="usage" title={title}>
      <span class="usage-name">{name}</span>
      <span class="usage-bar" role="img" aria-label={`${name}: ${u.text} of the block limit used`}><span style={{ width: `${u.pct}%` }} /></span>
      <span class="usage-text">{u.text}</span>
    </span>
  );
}

export function Pager({ offset, limit, total, onPage }: { offset: number; limit: number; total: number; onPage: (offset: number) => void }) {
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(total, offset + limit);
  return (
    <nav class="pager" aria-label="Pages">
      <button type="button" onClick={() => onPage(Math.max(0, offset - limit))} disabled={offset === 0}>Newer</button>
      <span role="status">{from.toLocaleString("en-US")} to {to.toLocaleString("en-US")} of {total.toLocaleString("en-US")}</span>
      <button type="button" onClick={() => onPage(offset + limit)} disabled={offset + limit >= total}>Older</button>
    </nav>
  );
}

export function Section({ id, title, children }: { id: string; title: string; children: ComponentChildren }) {
  return (
    <section class="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {children}
    </section>
  );
}

export function Facts({ rows }: { rows: [string, ComponentChildren][] }) {
  return (
    <dl class="glossary">
      {rows.map(([k, v]) => (
        <>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </>
      ))}
    </dl>
  );
}

/** Assets lists native tokens by policy and name; the full policy id is in the tooltip. */
export function Assets({ items }: { items?: Asset[] }) {
  if (!items || items.length === 0) return <span class="muted">none</span>;
  return (
    <ul class="tokens">
      {items.map((a) => (
        <li key={a.policy + a.name}>
          <span title={`policy ${a.policy}, name ${a.name || "(empty)"}`}>{a.quantity} {a.name_text ?? (a.name ? short(a.name, 8) : "(unnamed)")}</span>
          <span class="muted mono"> {short(a.policy, 8)}</span>
        </li>
      ))}
    </ul>
  );
}

/** OutputCells is the shared address, value, tokens, datum and script columns of an output row. */
export function OutputCells({ o, lovelace }: { o: Output; lovelace: string }) {
  return (
    <>
      <td><Addr value={o.address} /></td>
      <td>{lovelace}</td>
      <td><Assets items={o.assets} /></td>
      <td>
        {o.datum ? (
          <span title={o.datum.hex ?? o.datum.hash}>{o.datum.kind === "inline" ? "inline " : "hash "}<span class="mono">{short(o.datum.hash, 10)}</span></span>
        ) : (
          <span class="muted">none</span>
        )}
      </td>
      <td>{o.script_ref ? <span title={o.script_ref.hash}>{o.script_ref.language} <span class="mono">{short(o.script_ref.hash, 10)}</span></span> : <span class="muted">none</span>}</td>
    </>
  );
}
