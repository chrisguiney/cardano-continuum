import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { blockLink, bytesText, epochLink, forkLines, lovelaceText, nodeLink, stateText } from "../lib/explorer";
import type { BlockDetail } from "../lib/extypes";
import { simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link, Mono, Muted, Note, TableFrame, Title } from "./common";
import { BlockHash, Facts, HashLink, PoolChip, Section, TxHash } from "./exparts";

function PoolsOnBlock({ b }: { b: BlockDetail }) {
  if (b.pools.length === 0) return <Note>No node chains are followed here, so there is nothing to compare.</Note>;
  return (
    <>
      {forkLines(b.height, b.pools).map((l) => <p key={l} className="my-2 font-semibold text-destructive" role="status">{l}</p>)}
      <TableFrame>
        <Table>
          <TableHeader><TableRow><TableHead>Node</TableHead><TableHead>State</TableHead><TableHead>Its block at this height</TableHead></TableRow></TableHeader>
          <TableBody>
            {b.pools.map((p) => (
              <TableRow key={p.pool}>
                <TableCell><Link to={nodeLink(p.pool)}>{p.pool}</Link></TableCell>
                <TableCell><Badge variant={p.state === "fork" ? "bad" : p.state === "on" ? "default" : "warn"}>{stateText(p.state)}</Badge></TableCell>
                <TableCell>{p.hash ? (p.on_block ? <Muted>this block</Muted> : <BlockHash value={p.hash} />) : <Muted>none</Muted>}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableFrame>
    </>
  );
}

/** BlockBody shows one block: header fields, transactions and which pools have it. */
export function BlockBody({ b }: { b: BlockDetail }) {
  const h = b.header;
  return (
    <>
      {b.stub && <Note>This block is not in the index. It is known only because a node's chain has it, so the details below are limited to what the tip carries.</Note>}
      {b.evicted && <Note>This block is older than the blocks the explorer keeps in full, so its header is not available.</Note>}
      <Section id="b-head" title="Block">
        <Facts rows={[
          ["Height", String(b.height)],
          ["Slot", String(b.slot)],
          ["Epoch", <Link to={epochLink(b.epoch)}>{b.epoch}</Link>],
          ["Time", simTime(b.time_ms)],
          ["Hash", <HashLink value={b.hash} n={64} label="block hash" />],
          ["Previous", b.prev ? <BlockHash value={b.prev} n={64} /> : "none"],
          ["Next", b.next ? <BlockHash value={b.next} n={64} /> : <Muted>none yet</Muted>],
          ["Issuer (stake pool)", <PoolChip issuer={b.issuer} pool={b.pool} />],
          ["Size", bytesText(b.size)],
          ["Fees", lovelaceText(b.fees)],
          ["ExUnits used", `${b.ex_mem.toLocaleString("en-US")} memory, ${b.ex_steps.toLocaleString("en-US")} steps`],
        ]} />
      </Section>
      {h && h.body_hash && (
        <Section id="b-hdr" title="Header">
          <Facts rows={[
            ["Protocol version", `${h.proto_major}.${h.proto_minor}`],
            ["Body hash", <Mono>{h.body_hash}</Mono>],
            ["Body size", bytesText(h.body_size)],
            ["Issuer key", <Mono>{h.issuer_vkey}</Mono>],
            ["VRF key", <Mono>{h.vrf_key}</Mono>],
            ["Operational certificate", `sequence ${h.opcert_seq}, KES period ${h.kes_period}`],
            ["KES hot key", <Mono>{h.opcert_hot_vkey}</Mono>],
          ]} />
        </Section>
      )}
      <Section id="b-pools" title="Nodes on this block">
        <PoolsOnBlock b={b} />
      </Section>
      <Section id="b-txs" title={`Transactions (${b.txs.length})`}>
        {b.txs.length === 0 ? <Note>No transactions in this block.</Note> : (
          <TableFrame>
            <Table>
              <TableHeader><TableRow><TableHead>#</TableHead><TableHead>Hash</TableHead><TableHead>Fee</TableHead></TableRow></TableHeader>
              <TableBody>{b.txs.map((t, i) => <TableRow key={t.hash}><TableCell>{i}</TableCell><TableCell><TxHash value={t.hash} n={24} /></TableCell><TableCell>{lovelaceText(t.fee)}</TableCell></TableRow>)}</TableBody>
            </Table>
          </TableFrame>
        )}
      </Section>
    </>
  );
}

export function ExBlock({ id }: { id: string }) {
  const { data, error } = usePoll<BlockDetail>(`/api/explorer/blocks/${id}`, 5000);
  return (
    <>
      <Title>Block {data ? `#${data.height}` : ""}</Title>
      <Note><Link to="/explorer">All blocks</Link>{data?.prev && <> · <Link to={blockLink(data.prev)}>Previous block</Link></>}{data?.next && <> · <Link to={blockLink(data.next)}>Next block</Link></>}</Note>
      <ErrorNote error={error} />
      {data && <BlockBody b={data} />}
    </>
  );
}
