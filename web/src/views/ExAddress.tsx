import { useState, type ReactNode } from "react";
import { blockLink, lovelaceText } from "../lib/explorer";
import type { ActorRef, AddressDetail } from "../lib/extypes";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link } from "./common";
import { Addr, Assets, Facts, HashLink, OutputCells, Pager, Section, TxHash } from "./exparts";

const PAGE = 25;

function Owner({ a }: { a?: ActorRef }) {
  return a ? <Link to={`/actors/${a.id}`}>{a.name || `actor ${a.id}`}{a.role ? ` (${a.role})` : ""}</Link> : <span className="muted">no continuum actor</span>;
}

/** AddressBody shows an address's balance, owner, unspent outputs and history. */
export function AddressBody({ a, onPage }: { a: AddressDetail; onPage: (offset: number) => void }) {
  return (
    <>
      <Section id="a-head" title="Address">
        <Facts rows={[
          ["Address", <HashLink value={a.address} n={a.address.length} label="address" />],
          ["Owner", <Owner a={a.actor} />],
          ...(a.stake ? ([["Stake address", <span><Addr value={a.stake} /> <Owner a={a.stake_actor} /></span>]] as [string, ReactNode][]) : []),
          ["Balance", lovelaceText(a.balance)],
          ["Tokens", <Assets items={a.assets} />],
          ["Unspent outputs", String(a.utxo_total)],
          ["Transactions", String(a.tx_count)],
        ]} />
      </Section>
      <Section id="a-utxo" title={`Unspent outputs (${a.utxo_total})`}>
        {a.utxos.length === 0 ? <p className="muted">No unspent outputs.</p> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Output</th><th>Address</th><th>ADA</th><th>Tokens</th><th>Datum</th><th>Reference script</th></tr></thead>
              <tbody>{a.utxos.map((o) => <tr key={`${o.tx}#${o.index}`}><td><TxHash value={o.tx ?? ""} n={12} />{`#${o.index}`}</td><OutputCells o={o} lovelace={lovelaceText(o.lovelace)} /></tr>)}</tbody>
            </table>
          </div>
        )}
      </Section>
      <Section id="a-hist" title={`History (${a.tx_count})`}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>Transaction</th><th>Block</th><th>Slot</th></tr></thead>
            <tbody>{a.history.map((h) => <tr key={h.hash}><td><TxHash value={h.hash} n={24} /></td><td><Link to={blockLink(h.block)}>{`#${h.height}`}</Link></td><td>{h.slot}</td></tr>)}</tbody>
          </table>
        </div>
        <Pager offset={a.offset} limit={a.limit} total={Math.max(a.tx_count, a.utxo_total)} onPage={onPage} />
      </Section>
    </>
  );
}

export function ExAddress({ addr }: { addr: string }) {
  const [offset, setOffset] = useState(0);
  const { data, error } = usePoll<AddressDetail>(`/api/explorer/addresses/${addr}?limit=${PAGE}&offset=${offset}`, 10_000);
  return (
    <>
      <h1>Address</h1>
      <p className="muted"><Link to="/explorer">All blocks</Link></p>
      <ErrorNote error={error} />
      {data && <AddressBody a={data} onPage={setOffset} />}
    </>
  );
}


