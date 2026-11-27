import { ada, count } from "../lib/format";
import { groupParams, govPath } from "../lib/govdetail";
import { GOV_HELP } from "../lib/govhelp";
import type { GovAnchor, GovContent, GovParam } from "../lib/types";
import { Link } from "./common";
import { HelpTip } from "./fields";
import { Addr, HashLink } from "./exparts";

/** Tip is the "?" for a governance term; id keeps the tip unique when a term shows twice on a page. */
export function Tip({ k, id = k }: { k: string; id?: string }) {
  const h = GOV_HELP[k]!;
  return <HelpTip id={`gt-${id}`} label={h.label} text={h.help} />;
}

/** Term is a term's name with its tip. */
export function Term({ k, id }: { k: string; id?: string }) {
  return <>{GOV_HELP[k]!.label}<Tip k={k} id={id} /></>;
}

export function AnchorLine({ a }: { a?: GovAnchor }) {
  if (!a) return <span class="muted">none</span>;
  return (
    <>
      <span class="mono">{a.url || "no URL"}</span>
      <div class="muted">hash <span class="mono" title={a.hash}>{a.hash}</span></div>
    </>
  );
}

/** Account shows a reward address, linked to the continuum actor that owns it when there is one. */
export function Account({ address, actor }: { address: string; actor?: { id: number; name: string } }) {
  return (
    <>
      <Addr value={address} />
      {actor && <> <Link to={`/actors/${actor.id}`}>{actor.name} (actor {actor.id})</Link></>}
    </>
  );
}

function Params({ ps }: { ps: GovParam[] }) {
  return (
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Parameter</th><th><Term k="group" /></th><th><Term k="security_group" id="security-col" /></th>
            <th class="num">New value</th><th class="num"><Term k="param_current" /></th><th>Unit</th>
          </tr>
        </thead>
        <tbody>
          {groupParams(ps).flatMap(([, rows]) => rows).map((p) => (
            <tr key={p.field}>
              <td><strong>{p.name}</strong><div class="muted">{p.desc}</div></td>
              <td>{p.group || "other"}</td>
              <td>{p.security ? <span class="tag warn">security group</span> : <span class="muted">no</span>}</td>
              <td class="num">{p.new}</td>
              <td class="num">{p.current ?? <span class="muted">unknown</span>}</td>
              <td>{p.unit}</td>
            </tr>
          ))}
          {ps.length === 0 && <tr><td colSpan={6} class="empty">This action changes no parameter.</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function Treasury({ c }: { c: GovContent }) {
  return (
    <div class="table-wrap">
      <table>
        <thead><tr><th>Recipient</th><th class="num">Amount</th></tr></thead>
        <tbody>
          {(c.withdrawals ?? []).map((w) => (
            <tr key={w.address}><td><Account address={w.address} actor={w.actor} /></td><td class="num">{ada(w.lovelace)}</td></tr>
          ))}
          <tr><th scope="row">Total</th><td class="num"><strong>{ada(c.total ?? 0)}</strong></td></tr>
        </tbody>
      </table>
    </div>
  );
}

function Committee({ c }: { c: GovContent }) {
  const removed = c.removed ?? [];
  const added = c.added ?? [];
  return (
    <dl class="traits">
      <dt>Removed members</dt>
      <dd>{removed.length ? <ul>{removed.map((m) => <li key={m} class="mono">{m}</li>)}</ul> : <span class="muted">none</span>}</dd>
      <dt>Added members</dt>
      <dd>{added.length ? <ul>{added.map((m) => <li key={m.credential}><span class="mono">{m.credential}</span> term ends epoch {m.epoch}</li>)}</ul> : <span class="muted">none</span>}</dd>
      <dt><Term k="quorum" /></dt>
      <dd>{c.quorum || <span class="muted">unset</span>}</dd>
    </dl>
  );
}

/** Contents is what the action asks for, by kind. */
export function Contents({ c }: { c: GovContent }) {
  const what = GOV_HELP[c.kind];
  return (
    <>
      {what && <p class="muted">{what.help}</p>}
      {c.kind === "parameter_change" && <Params ps={c.params ?? []} />}
      {c.kind === "treasury_withdrawals" && <Treasury c={c} />}
      {c.kind === "update_committee" && <Committee c={c} />}
      {c.kind === "new_constitution" && c.constitution && (
        <dl class="traits">
          <dt><Term k="anchor" id="constitution-anchor" /></dt><dd><AnchorLine a={c.constitution.anchor} /></dd>
          <dt><Term k="guardrails" id="constitution-script" /></dt>
          <dd>{c.constitution.script_hash ? <HashLink value={c.constitution.script_hash} n={28} label="guardrails script hash" /> : <span class="muted">none</span>}</dd>
        </dl>
      )}
      {c.kind === "hard_fork_initiation" && c.version && (
        <dl class="traits"><dt><Term k="hard_fork" /></dt><dd><strong>{c.version.major}.{c.version.minor}</strong></dd></dl>
      )}
      {(c.kind === "parameter_change" || c.kind === "treasury_withdrawals") && c.policy_hash && (
        <dl class="traits">
          <dt><Term k="guardrails" /></dt>
          <dd><HashLink value={c.policy_hash} n={28} label="guardrails script hash" /></dd>
        </dl>
      )}
    </>
  );
}

/** PrevLink links the action this one builds on. */
export function PrevLink({ c }: { c: GovContent }) {
  if (!c.prev_action) return <span class="muted">none</span>;
  const to = govPath(`${c.prev_action.tx}#${c.prev_action.index}`);
  const text = `${c.prev_action.tx.slice(0, 10)}…#${c.prev_action.index}`;
  return to ? <Link to={to}>{text}</Link> : <span class="mono">{text}</span>;
}

export const votes = (n: number) => `${count(n)} ${n === 1 ? "vote" : "votes"}`;
