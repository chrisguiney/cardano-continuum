import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useState, type ReactNode } from "react";
import { blockLink, lovelaceText } from "../lib/explorer";
import type { ActorRef, AddressDetail } from "../lib/extypes";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link, Muted, Note, TableFrame, Title } from "./common";
import { Addr, Assets, Facts, HashLink, OutputCells, Pager, Section, TxHash } from "./exparts";

const PAGE = 25;

function Owner({ a }: { a?: ActorRef }) {
  return a ? <Link to={`/actors/${a.id}`}>{a.name || `actor ${a.id}`}{a.role ? ` (${a.role})` : ""}</Link> : <Muted>no continuum actor</Muted>;
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
        {a.utxos.length === 0 ? <Note>No unspent outputs.</Note> : (
          <TableFrame>
            <Table>
              <TableHeader><TableRow><TableHead>Output</TableHead><TableHead>Address</TableHead><TableHead>ADA</TableHead><TableHead>Tokens</TableHead><TableHead>Datum</TableHead><TableHead>Reference script</TableHead></TableRow></TableHeader>
              <TableBody>{a.utxos.map((o) => <TableRow key={`${o.tx}#${o.index}`}><TableCell><TxHash value={o.tx ?? ""} n={12} />{`#${o.index}`}</TableCell><OutputCells o={o} lovelace={lovelaceText(o.lovelace)} /></TableRow>)}</TableBody>
            </Table>
          </TableFrame>
        )}
      </Section>
      <Section id="a-hist" title={`History (${a.tx_count})`}>
        <TableFrame>
          <Table>
            <TableHeader><TableRow><TableHead>Transaction</TableHead><TableHead>Block</TableHead><TableHead>Slot</TableHead></TableRow></TableHeader>
            <TableBody>{a.history.map((h) => <TableRow key={h.hash}><TableCell><TxHash value={h.hash} n={24} /></TableCell><TableCell><Link to={blockLink(h.block)}>{`#${h.height}`}</Link></TableCell><TableCell>{h.slot}</TableCell></TableRow>)}</TableBody>
          </Table>
        </TableFrame>
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
      <Title>Address</Title>
      <Note><Link to="/explorer">All blocks</Link></Note>
      <ErrorNote error={error} />
      {data && <AddressBody a={data} onPage={setOffset} />}
    </>
  );
}


