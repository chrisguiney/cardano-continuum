import { buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { blockLink, lovelaceText, metadataText, txCborPath } from "../lib/explorer";
import type { Cert, Input, Output, TxDetail } from "../lib/extypes";
import { usePoll } from "../lib/hooks";
import { CopyButton, ErrorNote, Link, Mono, Muted, Note, Subheading, TableFrame, Title } from "./common";
import { Addr, Assets, Facts, HashLink, OutputCells, Section, TxHash } from "./exparts";

// PRE is a block of raw bytes or JSON: it wraps anywhere and scrolls past 14rem.
const PRE = "mt-2 max-h-56 overflow-auto rounded-md border bg-muted p-2.5 font-mono text-[0.85em] whitespace-pre-wrap [overflow-wrap:anywhere]";

const OUT_HEAD = <TableRow><TableHead>#</TableHead><TableHead>Address</TableHead><TableHead>ADA</TableHead><TableHead>Tokens</TableHead><TableHead>Datum</TableHead><TableHead>Reference script</TableHead></TableRow>;

function Inputs({ items, empty }: { items: Input[]; empty: string }) {
  if (items.length === 0) return <Note>{empty}</Note>;
  return (
    <TableFrame>
      <Table>
        <TableHeader><TableRow><TableHead>Spends</TableHead><TableHead>Address</TableHead><TableHead>ADA</TableHead><TableHead>Tokens</TableHead></TableRow></TableHeader>
        <TableBody>
          {items.map((i) => (
            <TableRow key={`${i.tx}#${i.index}`}>
              <TableCell><TxHash value={i.tx} n={14} />{`#${i.index}`}</TableCell>
              <TableCell>{i.resolved ? <Addr value={i.resolved.address} /> : <Muted>source not indexed</Muted>}</TableCell>
              <TableCell>{i.resolved ? lovelaceText(i.resolved.lovelace) : ""}</TableCell>
              <TableCell>{i.resolved ? <Assets items={i.resolved.assets} /> : ""}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function Outputs({ items }: { items: Output[] }) {
  if (items.length === 0) return <Note>None.</Note>;
  return (
    <TableFrame>
      <Table>
        <TableHeader>{OUT_HEAD}</TableHeader>
        <TableBody>{items.map((o) => <TableRow key={o.index}><TableCell>{o.index}</TableCell><OutputCells o={o} lovelace={lovelaceText(o.lovelace)} /></TableRow>)}</TableBody>
      </Table>
    </TableFrame>
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
        ["Submitted by", c.actor ? <Link to={`/actors/${c.actor.id}`}>{c.actor.name || `actor ${c.actor.id}`}</Link> : <Muted>not submitted by an actor</Muted>],
        ["Intent", c.intent ? `${c.intent}${c.kind ? ` (${c.kind})` : " (payment)"}` : "none"],
        ["Amount", lovelaceText(c.amount)],
        ["Oracle verdict", c.verdict ? <Badge>{c.verdict}{c.reason ? `: ${c.reason}` : ""}</Badge> : <Muted>none recorded</Muted>],
        ...(c.attack ? ([["Attack", c.attack]] as [string, string][]) : []),
        ["Findings", c.findings.length === 0 ? <Muted>none</Muted> : <ul>{c.findings.map((f) => <li key={f.id}><Link to="/findings"><Badge variant={f.severity === "fatal" ? "bad" : "warn"}>{f.severity}</Badge> {f.kind}</Link>: {f.summary}</li>)}</ul>],
      ]} />
    </Section>
  );
}

function Download({ tx }: { tx: TxDetail }) {
  const hex = tx.cbor_hex ?? "";
  return (
    <Section id="t-cbor" title="Raw CBOR">
      <p className="flex flex-wrap items-center gap-2">
        <CopyButton value={hex} label="transaction CBOR" />{" "}
        <a className={buttonVariants({ variant: "outline", size: "sm" })} href={txCborPath(tx.hash)} download={`${tx.hash}.cbor`}>Download .cbor</a>{" "}
        <Muted>{hex.length / 2} bytes</Muted>
      </p>
      <pre className={PRE} tabIndex={0} aria-label="Transaction CBOR in hexadecimal">{hex}</pre>
    </Section>
  );
}

function Tail({ tx }: { tx: TxDetail }) {
  return (
    <>
      {tx.certificates.length > 0 && (
        <Section id="t-certs" title="Certificates">
          <ul className="space-y-1">{tx.certificates.map((c, i) => <li key={i} className="[overflow-wrap:anywhere]"><Badge>{c.kind}</Badge> {certText(c)}</li>)}</ul>
        </Section>
      )}
      {tx.votes.length > 0 && (
        <Section id="t-votes" title="Votes">
          <ul className="space-y-1">{tx.votes.map((v, i) => <li key={i}><Badge>{v.choice}</Badge> by {v.voter} <Mono title={v.voter_hash}>{v.voter_hash.slice(0, 10)}</Mono> on <TxHash value={v.action_tx} n={12} /><Link to={`/governance/${v.action_tx}/${v.action_index}`}>{`#${v.action_index}`}</Link></li>)}</ul>
        </Section>
      )}
      {tx.proposals.length > 0 && (
        <Section id="t-props" title="Proposals">
          <ul className="space-y-1">{tx.proposals.map((p) => <li key={p.index}><Badge>{p.kind}</Badge> deposit {lovelaceText(p.deposit)}, returns to <Addr value={p.return_address} />{p.anchor && <> · anchor {p.anchor}</>} · <Link to={`/governance/${tx.hash}/${p.index}`}>details and votes</Link></li>)}</ul>
        </Section>
      )}
      {tx.withdrawals.length > 0 && (
        <Section id="t-wd" title="Withdrawals">
          <ul className="space-y-1">{tx.withdrawals.map((w) => <li key={w.address}><Addr value={w.address} /> {lovelaceText(w.lovelace)}</li>)}</ul>
        </Section>
      )}
      {tx.metadata && <Section id="t-meta" title="Metadata"><pre className={PRE} tabIndex={0}>{metadataText(tx.metadata)}</pre></Section>}
    </>
  );
}

function Scripts({ tx }: { tx: TxDetail }) {
  if (tx.redeemers.length === 0 && tx.scripts.length === 0 && tx.required_signers.length === 0) return null;
  return (
    <Section id="t-scripts" title="Scripts, redeemers and signers">
      {tx.scripts.length > 0 && <ul className="space-y-1">{tx.scripts.map((s) => <li key={s.hash} className="[overflow-wrap:anywhere]"><Badge>{s.language}</Badge> <Mono>{s.hash}</Mono> ({s.size} bytes)</li>)}</ul>}
      {tx.redeemers.length > 0 && (
        <TableFrame>
          <Table>
            <TableHeader><TableRow><TableHead>Purpose</TableHead><TableHead>Index</TableHead><TableHead>Memory</TableHead><TableHead>Steps</TableHead><TableHead>Data</TableHead></TableRow></TableHeader>
            <TableBody>{tx.redeemers.map((r) => <TableRow key={r.tag + r.index}><TableCell>{r.tag}</TableCell><TableCell>{r.index}</TableCell><TableCell>{r.mem.toLocaleString("en-US")}</TableCell><TableCell>{r.steps.toLocaleString("en-US")}</TableCell><TableCell><Mono>{r.data_hex ?? ""}</Mono></TableCell></TableRow>)}</TableBody>
          </Table>
        </TableFrame>
      )}
      {tx.required_signers.length > 0 && <p className="my-2 [overflow-wrap:anywhere]">Required signers: {tx.required_signers.map((s) => <Mono key={s}>{s} </Mono>)}</p>}
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
          ["Phase-2 validity", tx.valid ? <Badge>valid</Badge> : <Badge variant="bad" title="isValid is false: it spent its collateral and had no other effect">invalid (collateral spent)</Badge>],
          ...(tx.evicted ? [] : ([["Size", `${tx.size} bytes`], ["Valid from slot", tx.valid_from === undefined ? "any" : String(tx.valid_from)], ["Valid until slot (TTL)", tx.ttl === undefined ? "no limit" : String(tx.ttl)]] as [string, string][])),
        ]} />
      </Section>
      {tx.evicted && <Note>The block holding this transaction is older than the blocks the explorer keeps in full, so only its header is known.</Note>}
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
          <Subheading>Reference inputs</Subheading>
          <Inputs items={tx.reference_inputs} empty="None." />
          <Subheading>Collateral</Subheading>
          <Inputs items={tx.collateral} empty="None." />
          {tx.total_collateral !== undefined && <p className="my-2">Total collateral: {lovelaceText(tx.total_collateral)}</p>}
          {tx.collateral_return && <><Subheading>Collateral return</Subheading><Outputs items={[tx.collateral_return]} /></>}
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
      <Title>Transaction</Title>
      <Note><Link to="/explorer">All blocks</Link>{data && <> · <Link to={blockLink(data.block)}>Its block</Link></>}</Note>
      <ErrorNote error={error} />
      {data && <TxBody tx={data} />}
    </>
  );
}


