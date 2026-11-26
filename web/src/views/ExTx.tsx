import { blockLink, lovelaceText, metadataText, txCborPath } from "../lib/explorer";
import type { Cert, Input, Output, TxDetail } from "../lib/extypes";
import { usePoll } from "../lib/hooks";
import { CopyButton, ErrorNote, Link } from "./common";
import { Addr, Assets, Facts, HashLink, OutputCells, Section, TxHash } from "./exparts";

const OUT_HEAD = <tr><th>#</th><th>Address</th><th>ADA</th><th>Tokens</th><th>Datum</th><th>Reference script</th></tr>;

function Inputs({ items, empty }: { items: Input[]; empty: string }) {
  if (items.length === 0) return <p class="muted">{empty}</p>;
  return (
    <div class="table-wrap">
      <table>
        <thead><tr><th>Spends</th><th>Address</th><th>ADA</th><th>Tokens</th></tr></thead>
        <tbody>
          {items.map((i) => (
            <tr key={`${i.tx}#${i.index}`}>
              <td><TxHash value={i.tx} n={14} />{`#${i.index}`}</td>
              <td>{i.resolved ? <Addr value={i.resolved.address} /> : <span class="muted">source not indexed</span>}</td>
              <td>{i.resolved ? lovelaceText(i.resolved.lovelace) : ""}</td>
              <td>{i.resolved ? <Assets items={i.resolved.assets} /> : ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Outputs({ items }: { items: Output[] }) {
  if (items.length === 0) return <p class="muted">None.</p>;
  return (
    <div class="table-wrap">
      <table>
        <thead>{OUT_HEAD}</thead>
        <tbody>{items.map((o) => <tr key={o.index}><td>{o.index}</td><OutputCells o={o} lovelace={lovelaceText(o.lovelace)} /></tr>)}</tbody>
      </table>
    </div>
  );
}

function certText(c: Cert): string {
  const parts = [c.credential && `credential ${c.credential}`, c.pool && `pool ${c.pool}`, c.drep && `DRep ${c.drep}`, c.amount ? `deposit ${lovelaceText(c.amount)}` : "", c.epoch ? `epoch ${c.epoch}` : "", c.anchor && `anchor ${c.anchor}`];
  return parts.filter(Boolean).join(", ");
}

function Context({ tx }: { tx: TxDetail }) {
  const c = tx.continuum;
  if (!c) return null;
  return (
    <Section id="t-ctx" title="Continuum context">
      <Facts rows={[
        ["Submitted by", c.actor ? <Link to={`/actors/${c.actor.id}`}>{c.actor.name || `actor ${c.actor.id}`}</Link> : <span class="muted">not submitted by an actor</span>],
        ["Intent", c.intent ? `${c.intent}${c.kind ? ` (${c.kind})` : " (payment)"}` : "none"],
        ["Amount", lovelaceText(c.amount)],
        ["Oracle verdict", c.verdict ? <span class="tag">{c.verdict}{c.reason ? `: ${c.reason}` : ""}</span> : <span class="muted">none recorded</span>],
        ...(c.attack ? ([["Attack", c.attack]] as [string, string][]) : []),
        ["Findings", c.findings.length === 0 ? <span class="muted">none</span> : <ul>{c.findings.map((f) => <li key={f.id}><Link to="/findings"><span class={`tag ${f.severity === "fatal" ? "bad" : "warn"}`}>{f.severity}</span> {f.kind}</Link>: {f.summary}</li>)}</ul>],
      ]} />
    </Section>
  );
}

function Download({ tx }: { tx: TxDetail }) {
  const hex = tx.cbor_hex ?? "";
  return (
    <Section id="t-cbor" title="Raw CBOR">
      <p>
        <CopyButton value={hex} label="transaction CBOR" />{" "}
        <a class="button" href={txCborPath(tx.hash)} download={`${tx.hash}.cbor`}>Download .cbor</a>{" "}
        <span class="muted">{hex.length / 2} bytes</span>
      </p>
      <pre class="cbor" tabIndex={0} aria-label="Transaction CBOR in hexadecimal">{hex}</pre>
    </Section>
  );
}

function Tail({ tx }: { tx: TxDetail }) {
  return (
    <>
      {tx.certificates.length > 0 && (
        <Section id="t-certs" title="Certificates">
          <ul>{tx.certificates.map((c, i) => <li key={i}><span class="tag">{c.kind}</span> {certText(c)}</li>)}</ul>
        </Section>
      )}
      {tx.votes.length > 0 && (
        <Section id="t-votes" title="Votes">
          <ul>{tx.votes.map((v, i) => <li key={i}><span class="tag">{v.choice}</span> by {v.voter} <span class="mono" title={v.voter_hash}>{v.voter_hash.slice(0, 10)}</span> on <TxHash value={v.action_tx} n={12} />{`#${v.action_index}`}</li>)}</ul>
        </Section>
      )}
      {tx.proposals.length > 0 && (
        <Section id="t-props" title="Proposals">
          <ul>{tx.proposals.map((p) => <li key={p.index}><span class="tag">{p.kind}</span> deposit {lovelaceText(p.deposit)}, returns to <Addr value={p.return_address} />{p.anchor && <> · anchor {p.anchor}</>}</li>)}</ul>
        </Section>
      )}
      {tx.withdrawals.length > 0 && (
        <Section id="t-wd" title="Withdrawals">
          <ul>{tx.withdrawals.map((w) => <li key={w.address}><Addr value={w.address} /> {lovelaceText(w.lovelace)}</li>)}</ul>
        </Section>
      )}
      {tx.metadata && <Section id="t-meta" title="Metadata"><pre class="cbor" tabIndex={0}>{metadataText(tx.metadata)}</pre></Section>}
    </>
  );
}

function Scripts({ tx }: { tx: TxDetail }) {
  if (tx.redeemers.length === 0 && tx.scripts.length === 0 && tx.required_signers.length === 0) return null;
  return (
    <Section id="t-scripts" title="Scripts, redeemers and signers">
      {tx.scripts.length > 0 && <ul>{tx.scripts.map((s) => <li key={s.hash}><span class="tag">{s.language}</span> <span class="mono">{s.hash}</span> ({s.size} bytes)</li>)}</ul>}
      {tx.redeemers.length > 0 && (
        <div class="table-wrap">
          <table>
            <thead><tr><th>Purpose</th><th>Index</th><th>Memory</th><th>Steps</th><th>Data</th></tr></thead>
            <tbody>{tx.redeemers.map((r) => <tr key={r.tag + r.index}><td>{r.tag}</td><td>{r.index}</td><td>{r.mem.toLocaleString("en-US")}</td><td>{r.steps.toLocaleString("en-US")}</td><td class="mono">{r.data_hex ?? ""}</td></tr>)}</tbody>
          </table>
        </div>
      )}
      {tx.required_signers.length > 0 && <p>Required signers: {tx.required_signers.map((s) => <span key={s} class="mono">{s} </span>)}</p>}
    </Section>
  );
}

/** TxBody shows one transaction. A transaction whose block is evicted shows its header only. */
export function TxBody({ tx }: { tx: TxDetail }) {
  return (
    <>
      <Section id="t-head" title="Transaction">
        <Facts rows={[
          ["Hash", <HashLink value={tx.hash} n={64} label="transaction hash" />],
          ["Block", <Link to={blockLink(tx.block)}>{`#${tx.height}`}</Link>],
          ["Slot", String(tx.slot)],
          ["Position in block", String(tx.index)],
          ["Fee", lovelaceText(tx.fee)],
          ["Phase-2 validity", tx.valid ? <span class="tag">valid</span> : <span class="tag bad" title="isValid is false: it spent its collateral and had no other effect">invalid (collateral spent)</span>],
          ...(tx.evicted ? [] : ([["Size", `${tx.size} bytes`], ["Valid from slot", tx.valid_from === undefined ? "any" : String(tx.valid_from)], ["Valid until slot (TTL)", tx.ttl === undefined ? "no limit" : String(tx.ttl)]] as [string, string][])),
        ]} />
      </Section>
      {tx.evicted && <p class="muted">The block holding this transaction is older than the blocks the explorer keeps in full, so only its header is known.</p>}
      <Context tx={tx} />
      {!tx.evicted && <TxDetailBody tx={tx} />}
    </>
  );
}

function TxDetailBody({ tx }: { tx: TxDetail }) {
  return (
    <>
      <Section id="t-in" title={`Inputs (${tx.inputs.length})`}><Inputs items={tx.inputs} empty="None." /></Section>
      <Section id="t-out" title={`Outputs (${tx.outputs.length})`}><Outputs items={tx.outputs} /></Section>
      {(tx.reference_inputs.length > 0 || tx.collateral.length > 0 || tx.collateral_return) && (
        <Section id="t-col" title="Reference inputs and collateral">
          <h3>Reference inputs</h3>
          <Inputs items={tx.reference_inputs} empty="None." />
          <h3>Collateral</h3>
          <Inputs items={tx.collateral} empty="None." />
          {tx.total_collateral !== undefined && <p>Total collateral: {lovelaceText(tx.total_collateral)}</p>}
          {tx.collateral_return && <><h3>Collateral return</h3><Outputs items={[tx.collateral_return]} /></>}
        </Section>
      )}
      {tx.mint.length > 0 && <Section id="t-mint" title="Mint and burn"><Assets items={tx.mint} /></Section>}
      <Tail tx={tx} />
      <Scripts tx={tx} />
      <Download tx={tx} />
    </>
  );
}

export function ExTx({ id }: { id: string }) {
  const { data, error } = usePoll<TxDetail>(`/api/explorer/txs/${id}`, 10_000);
  return (
    <>
      <h1>Transaction</h1>
      <p class="muted"><Link to="/explorer">All blocks</Link>{data && <> · <Link to={blockLink(data.block)}>Its block</Link></>}</p>
      <ErrorNote error={error} />
      {data && <TxBody tx={data} />}
    </>
  );
}


