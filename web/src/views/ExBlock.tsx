import { blockLink, bytesText, epochLink, forkLines, lovelaceText, stateText } from "../lib/explorer";
import type { BlockDetail } from "../lib/extypes";
import { simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link } from "./common";
import { BlockHash, Facts, HashLink, PoolChip, Section, TxHash } from "./exparts";

function PoolsOnBlock({ b }: { b: BlockDetail }) {
  if (b.pools.length === 0) return <p className="muted">No pool chains are followed here, so there is nothing to compare.</p>;
  return (
    <>
      {forkLines(b.height, b.pools).map((l) => <p key={l} className="error" role="status">{l}</p>)}
      <div className="table-wrap">
        <table>
          <thead><tr><th>Pool</th><th>State</th><th>Its block at this height</th></tr></thead>
          <tbody>
            {b.pools.map((p) => (
              <tr key={p.pool}>
                <td><Link to="/pools">{p.pool}</Link></td>
                <td><span className={`tag ${p.state === "fork" ? "bad" : p.state === "on" ? "" : "warn"}`}>{stateText(p.state)}</span></td>
                <td>{p.hash ? (p.on_block ? <span className="muted">this block</span> : <BlockHash value={p.hash} />) : <span className="muted">none</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

/** BlockBody shows one block: header fields, transactions and which pools have it. */
export function BlockBody({ b }: { b: BlockDetail }) {
  const h = b.header;
  return (
    <>
      {b.stub && <p className="muted">This block is not in the index. It is known only because a pool's chain has it, so the details below are limited to what the tip carries.</p>}
      {b.evicted && <p className="muted">This block is older than the blocks the explorer keeps in full, so its header is not available.</p>}
      <Section id="b-head" title="Block">
        <Facts rows={[
          ["Height", String(b.height)],
          ["Slot", String(b.slot)],
          ["Epoch", <Link to={epochLink(b.epoch)}>{b.epoch}</Link>],
          ["Time", simTime(b.time_ms)],
          ["Hash", <HashLink value={b.hash} n={64} label="block hash" />],
          ["Previous", b.prev ? <BlockHash value={b.prev} n={64} /> : "none"],
          ["Next", b.next ? <BlockHash value={b.next} n={64} /> : <span className="muted">none yet</span>],
          ["Issuer pool", <PoolChip issuer={b.issuer} pool={b.pool} />],
          ["Size", bytesText(b.size)],
          ["Fees", lovelaceText(b.fees)],
          ["ExUnits used", `${b.ex_mem.toLocaleString("en-US")} memory, ${b.ex_steps.toLocaleString("en-US")} steps`],
        ]} />
      </Section>
      {h && h.body_hash && (
        <Section id="b-hdr" title="Header">
          <Facts rows={[
            ["Protocol version", `${h.proto_major}.${h.proto_minor}`],
            ["Body hash", <span className="mono">{h.body_hash}</span>],
            ["Body size", bytesText(h.body_size)],
            ["Issuer key", <span className="mono">{h.issuer_vkey}</span>],
            ["VRF key", <span className="mono">{h.vrf_key}</span>],
            ["Operational certificate", `sequence ${h.opcert_seq}, KES period ${h.kes_period}`],
            ["KES hot key", <span className="mono">{h.opcert_hot_vkey}</span>],
          ]} />
        </Section>
      )}
      <Section id="b-pools" title="Pools on this block">
        <PoolsOnBlock b={b} />
      </Section>
      <Section id="b-txs" title={`Transactions (${b.txs.length})`}>
        {b.txs.length === 0 ? <p className="muted">No transactions in this block.</p> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>#</th><th>Hash</th><th>Fee</th></tr></thead>
              <tbody>{b.txs.map((t, i) => <tr key={t.hash}><td>{i}</td><td><TxHash value={t.hash} n={24} /></td><td>{lovelaceText(t.fee)}</td></tr>)}</tbody>
            </table>
          </div>
        )}
      </Section>
    </>
  );
}

export function ExBlock({ id }: { id: string }) {
  const { data, error } = usePoll<BlockDetail>(`/api/explorer/blocks/${id}`, 5000);
  return (
    <>
      <h1>Block {data ? `#${data.height}` : ""}</h1>
      <p className="muted"><Link to="/explorer">All blocks</Link>{data?.prev && <> · <Link to={blockLink(data.prev)}>Previous block</Link></>}{data?.next && <> · <Link to={blockLink(data.next)}>Next block</Link></>}</p>
      <ErrorNote error={error} />
      {data && <BlockBody b={data} />}
    </>
  );
}
