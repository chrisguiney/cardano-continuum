import { usePoll } from "../lib/hooks";
import { ada, count, short } from "../lib/format";
import { CLASSES, choiceLine, yesShare } from "../lib/governance";
import { govApi, securityNames, voterLink } from "../lib/govdetail";
import type { GovDetail, GovVote } from "../lib/types";
import { ErrorNote, Link } from "./common";
import { HelpTip } from "./fields";
import { TxHash } from "./exparts";
import { Account, AnchorLine, Contents, PrevLink, Term, Tip } from "./govparts";

const CLASS_LABEL: Record<string, string> = { drep: "DRep", spo: "SPO", committee: "Constitutional committee" };

function Header({ d }: { d: GovDetail }) {
  const c = d.content;
  const p = d.proposal;
  return (
    <section class="card" aria-labelledby="g-head">
      <h2 id="g-head">Header</h2>
      <dl class="traits">
        <dt>Action type</dt><dd><span class="tag">{p.type || "unknown"}</span></dd>
        {c && <><dt><Term k="return_address" /></dt><dd><Account address={c.return_address} actor={c.return_actor} /></dd></>}
        {!c && p.proposer !== undefined && <><dt>Proposer</dt><dd><Link to={`/actors/${p.proposer}`}>actor {p.proposer}</Link></dd></>}
        <dt><Term k="deposit" /></dt><dd>{ada(p.deposit_lovelace)}</dd>
        <dt>Submitted</dt><dd>{p.epoch === undefined ? "epoch unknown" : `epoch ${p.epoch}`}, slot {count(p.slot)} · transaction <TxHash value={d.id.split("#")[0]!} n={12} /></dd>
        <dt><Term k="gov_lifetime" /></dt>
        <dd>{p.expires_epoch === undefined ? "–" : `last epoch ${p.expires_epoch}`} <span class="muted">({d.lifetime_epochs} epochs from submission{d.epoch !== undefined ? `, now epoch ${d.epoch}` : ""})</span></dd>
        <dt><Term k="status" /></dt>
        <dd><span class={`tag ${p.status === "open" ? "ok" : "warn"}`}>{p.status}</span> <span class="muted">not ratified here <Tip k="ratification" /></span></dd>
        {c && <><dt><Term k="anchor" /></dt><dd><AnchorLine a={c.anchor} /></dd></>}
        {c && <><dt><Term k="prev_action" /></dt><dd><PrevLink c={c} /></dd></>}
      </dl>
    </section>
  );
}

function Eligibility({ d }: { d: GovDetail }) {
  const sec = d.content?.params ? securityNames(d.content.params) : [];
  return (
    <section class="card" aria-labelledby="g-who">
      <h2 id="g-who"><Term k="eligibility" /></h2>
      {sec.length > 0 && <p class="muted">This change touches the security group ({sec.join(", ")}), so stake pools vote too.</p>}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Voter</th><th>May vote</th><th>Why</th></tr></thead>
          <tbody>
            {d.voters.map((v) => (
              <tr key={v.class}>
                <td>{GOV_LABEL(v.class)}</td>
                <td>{v.allowed ? <span class="tag ok">yes</span> : <span class="tag warn">not eligible</span>}</td>
                <td>{v.reason} <HelpTip id={`rule-${v.class}`} label={`${CLASS_LABEL[v.class] ?? v.class} rule`} text={`${v.reason} Rule: ${v.rule}, cardano-ledger.`} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

const GOV_LABEL = (c: string) => CLASS_LABEL[c] ?? c;

function Tallies({ d }: { d: GovDetail }) {
  return (
    <div class="table-wrap">
      <table>
        <thead><tr><th>Class</th><th>Votes <Tip k="tally" /></th><th>Share</th></tr></thead>
        <tbody>
          {CLASSES.map((c) => {
            const t = d.proposal.tally[c];
            const eligible = d.voters.find((v) => v.class === c)?.allowed ?? true;
            const share = t ? yesShare(t) : null;
            return (
              <tr key={c}>
                <td>{c === "drep" ? <Term k="drep" id="tally-drep" /> : c === "spo" ? <Term k="spo" id="tally-spo" /> : <Term k="committee" id="tally-committee" />}</td>
                <td>{!eligible ? <span class="muted">not eligible</span> : t ? choiceLine(t) : "no votes"}</td>
                <td>{eligible && share !== null ? `${(share * 100).toFixed(0)}% yes` : <span class="muted">–</span>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function VoteList({ votes }: { votes: GovVote[] }) {
  return (
    <div class="table-wrap">
      <table>
        <thead><tr><th>Slot</th><th>Voter</th><th>Id</th><th>Choice</th><th class="num"><Term k="voting_power" /></th><th>Transaction</th><th>Anchor</th></tr></thead>
        <tbody>
          {votes.map((v) => {
            const to = voterLink(v.actor);
            return (
              <tr key={`${v.tx}-${v.voter}-${v.voter_id}`}>
                <td class="num">{count(v.slot)}</td>
                <td>{GOV_LABEL(v.voter)}</td>
                <td>{to ? <Link to={to}>actor {v.actor}</Link> : <span class="mono" title={v.voter_id}>{short(v.voter_id, 10)}</span>}{v.profile && <span class="muted"> {v.profile}</span>}</td>
                <td><span class="tag">{v.choice}</span></td>
                <td class="num">{v.power > 0 ? ada(v.power) : "–"}</td>
                <td><TxHash value={v.tx} n={10} /></td>
                <td>{v.anchor ? <span class="mono" title={v.anchor.hash}>{v.anchor.url}</span> : <span class="muted">none</span>}</td>
              </tr>
            );
          })}
          {votes.length === 0 && <tr><td colSpan={7} class="empty">No votes on this action yet.</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

/** GovBody is one governance action in full: header, contents, who may vote and the votes. */
export function GovBody({ d }: { d: GovDetail }) {
  return (
    <>
      <h1><Term k="governance_action" /> <span class="muted">{d.proposal.type}</span></h1>
      {d.summary && <p class="blurb">{d.summary}</p>}
      <Header d={d} />
      <section class="card" aria-labelledby="g-body">
        <h2 id="g-body">What it asks for</h2>
        {d.content ? <Contents c={d.content} /> : <p class="muted">The explorer has not decoded this action (its transaction is not in the index), so only the type is known.</p>}
      </section>
      <Eligibility d={d} />
      <section class="card" aria-labelledby="g-votes">
        <h2 id="g-votes">Votes</h2>
        <Tallies d={d} />
        <VoteList votes={d.votes} />
      </section>
    </>
  );
}

export function GovAction({ tx, index }: { tx: string; index: number }) {
  const { data, error } = usePoll<GovDetail>(govApi(tx, index), 3000);
  return (
    <>
      <p><Link to="/governance">All governance actions</Link></p>
      <ErrorNote error={error} />
      {!data && !error && <p class="muted">Loading…</p>}
      {data && <GovBody d={data} />}
    </>
  );
}
