import { useState } from "preact/hooks";
import { BLOCK_PAGE, blocksPath, bytesText, epochLink, exUnitsText, lovelaceText } from "../lib/explorer";
import type { BlockPage, BlockRow } from "../lib/extypes";

import { simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { VIEW_BLURB } from "../lib/help";
import { ErrorNote, Link } from "./common";
import { Blurb } from "./fields";
import { BlockHash, Pager, PoolChip, UsageBar } from "./exparts";

export function BlockTable({ page }: { page: BlockPage }) {
  return (
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Height</th><th>Slot</th><th>Epoch</th><th>Time</th><th>Hash</th><th>Issuer</th><th>Txs</th><th>Size</th><th>Fees</th><th>ExUnits used</th></tr>
        </thead>
        <tbody>
          {page.rows.map((b) => <BlockRowView key={b.hash} b={b} page={page} />)}
          {page.rows.length === 0 && <tr><td colSpan={10} class="empty">No blocks indexed yet.</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function BlockRowView({ b, page }: { b: BlockRow; page: BlockPage }) {
  return (
    <tr>
      <td>{b.height}</td>
      <td>{b.slot}</td>
      <td><Link to={epochLink(b.epoch)}>{b.epoch}</Link></td>
      <td>{simTime(b.time_ms)}</td>
      <td><BlockHash value={b.hash} /></td>
      <td><PoolChip issuer={b.issuer} pool={b.pool} /></td>
      <td>{b.txs}</td>
      <td title={`${b.size} bytes of ${page.limits.max_bytes}`}>{bytesText(b.size)}</td>
      <td>{lovelaceText(b.fees)}</td>
      <td>
        <UsageBar name="memory" used={b.ex_mem} limit={page.limits.max_mem} title={exUnitsText(b, page.limits)} />
        <UsageBar name="steps" used={b.ex_steps} limit={page.limits.max_steps} title={exUnitsText(b, page.limits)} />
      </td>
    </tr>
  );
}

/**
 * Explorer lists blocks, newest first. The newest page refreshes every few
 * seconds; an older page stays put so rows do not shift under the reader.
 */
export function Explorer({ pool = "" }: { pool?: string }) {
  const [offset, setOffset] = useState(0);
  const live = offset === 0;
  const { data, error } = usePoll<BlockPage>(blocksPath(offset, pool), live ? 3000 : 60_000);
  return (
    <>
      <h1>{pool ? "Blocks by pool" : "Explorer"}</h1>
      <Blurb text={VIEW_BLURB.explorer!} />
      {pool && <p class="muted">Pool <span class="mono">{pool}</span>. <Link to="/pools">Pools view</Link></p>}
      <p class="muted" role="status">{live ? "Live: the newest blocks refresh every 3 seconds." : "Paused: go to the newest page to follow the chain."}</p>
      <ErrorNote error={error} />
      {data && <BlockTable page={data} />}
      {data && <Pager offset={offset} limit={BLOCK_PAGE} total={data.total} onPage={setOffset} />}
    </>
  );
}
