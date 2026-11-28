import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useState } from "react";
import { BLOCK_PAGE, blocksPath, bytesText, epochLink, exUnitsText, lovelaceText } from "../lib/explorer";
import type { BlockPage, BlockRow } from "../lib/extypes";

import { simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { VIEW_BLURB } from "../lib/help";
import { EmptyRow, ErrorNote, Link, Mono, Note, TableFrame, Title } from "./common";
import { Blurb } from "./fields";
import { BlockHash, Pager, PoolChip, UsageBar } from "./exparts";

export function BlockTable({ page }: { page: BlockPage }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow><TableHead>Height</TableHead><TableHead>Slot</TableHead><TableHead>Epoch</TableHead><TableHead>Time</TableHead><TableHead>Hash</TableHead><TableHead>Issuer</TableHead><TableHead>Txs</TableHead><TableHead>Size</TableHead><TableHead>Fees</TableHead><TableHead>ExUnits used</TableHead></TableRow>
        </TableHeader>
        <TableBody>
          {page.rows.map((b) => <BlockRowView key={b.hash} b={b} page={page} />)}
          {page.rows.length === 0 && <EmptyRow cols={10}>No blocks indexed yet.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function BlockRowView({ b, page }: { b: BlockRow; page: BlockPage }) {
  return (
    <TableRow>
      <TableCell>{b.height}</TableCell>
      <TableCell>{b.slot}</TableCell>
      <TableCell><Link to={epochLink(b.epoch)}>{b.epoch}</Link></TableCell>
      <TableCell>{simTime(b.time_ms)}</TableCell>
      <TableCell><BlockHash value={b.hash} /></TableCell>
      <TableCell><PoolChip issuer={b.issuer} pool={b.pool} /></TableCell>
      <TableCell>{b.txs}</TableCell>
      <TableCell title={`${b.size} bytes of ${page.limits.max_bytes}`}>{bytesText(b.size)}</TableCell>
      <TableCell>{lovelaceText(b.fees)}</TableCell>
      <TableCell>
        <UsageBar name="memory" used={b.ex_mem} limit={page.limits.max_mem} title={exUnitsText(b, page.limits)} />
        <UsageBar name="steps" used={b.ex_steps} limit={page.limits.max_steps} title={exUnitsText(b, page.limits)} />
      </TableCell>
    </TableRow>
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
      <Title>{pool ? "Blocks by pool" : "Explorer"}</Title>
      <Blurb text={VIEW_BLURB.explorer!} />
      {pool && <Note>Pool <Mono>{pool}</Mono>. <Link to="/pools">Pools view</Link></Note>}
      <Note role="status">{live ? "Live: the newest blocks refresh every 3 seconds." : "Paused: go to the newest page to follow the chain."}</Note>
      <ErrorNote error={error} />
      {data && <BlockTable page={data} />}
      {data && <Pager offset={offset} limit={BLOCK_PAGE} total={data.total} onPage={setOffset} />}
    </>
  );
}
