import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { epochLink, bytesText, lovelaceText, poolLink } from "../lib/explorer";
import type { EpochView } from "../lib/extypes";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link, Mono, Muted, Note, TableFrame, Title } from "./common";
import { Facts, Section } from "./exparts";

/** EpochBody summarises an epoch: totals, and each pool's blocks against an equal share. */
export function EpochBody({ e }: { e: EpochView }) {
  return (
    <>
      <Section id="e-head" title={`Epoch ${e.epoch}`}>
        <Facts rows={[
          ["Blocks", count(e.blocks)],
          ["Heights", `${e.first_height} to ${e.last_height}`],
          ["Transactions", count(e.txs)],
          ["Fees", lovelaceText(e.fees)],
          ["Block data", bytesText(e.size)],
        ]} />
      </Section>
      <Section id="e-pools" title="Blocks per pool against expected">
        <Note>The explorer does not see stake, so expected is an equal share of the epoch's blocks among the pools.</Note>
        <TableFrame>
          <Table>
            <TableHeader><TableRow><TableHead>Pool</TableHead><TableHead>Blocks</TableHead><TableHead>Expected</TableHead></TableRow></TableHeader>
            <TableBody>
              {e.pools.map((p) => (
                <TableRow key={p.pool}>
                  <TableCell><Link to={poolLink(p.pool)}>{p.resolved ? <>{p.resolved.name} <Muted>({p.resolved.impl})</Muted></> : <Mono title={p.pool}>{p.pool.slice(0, 12)}…</Mono>}</Link></TableCell>
                  <TableCell>{p.blocks}</TableCell>
                  <TableCell>{p.expected.toFixed(1)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableFrame>
      </Section>
    </>
  );
}

export function ExEpoch({ n }: { n: number }) {
  const { data, error } = usePoll<EpochView>(`/api/explorer/epochs/${n}`, 10_000);
  return (
    <>
      <Title>Epoch {n}</Title>
      <Note>
        <Link to="/explorer">All blocks</Link>
        {n > 0 && <> · <Link to={epochLink(n - 1)}>Previous epoch</Link></>} · <Link to={epochLink(n + 1)}>Next epoch</Link>
      </Note>
      <ErrorNote error={error} />
      {data && <EpochBody e={data} />}
    </>
  );
}
