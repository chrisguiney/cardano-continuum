import { epochLink, bytesText, lovelaceText, poolLink } from "../lib/explorer";
import type { EpochView } from "../lib/extypes";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { ErrorNote, Link } from "./common";
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
        <p class="muted">The explorer does not see stake, so expected is an equal share of the epoch's blocks among the pools.</p>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Pool</th><th>Blocks</th><th>Expected</th></tr></thead>
            <tbody>
              {e.pools.map((p) => (
                <tr key={p.pool}>
                  <td><Link to={poolLink(p.pool)}>{p.resolved ? <>{p.resolved.name} <span class="muted">({p.resolved.impl})</span></> : <span class="mono" title={p.pool}>{p.pool.slice(0, 12)}…</span>}</Link></td>
                  <td>{p.blocks}</td>
                  <td>{p.expected.toFixed(1)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Section>
    </>
  );
}

export function ExEpoch({ n }: { n: number }) {
  const { data, error } = usePoll<EpochView>(`/api/explorer/epochs/${n}`, 10_000);
  return (
    <>
      <h1>Epoch {n}</h1>
      <p class="muted">
        <Link to="/explorer">All blocks</Link>
        {n > 0 && <> · <Link to={epochLink(n - 1)}>Previous epoch</Link></>} · <Link to={epochLink(n + 1)}>Next epoch</Link>
      </p>
      <ErrorNote error={error} />
      {data && <EpochBody e={data} />}
    </>
  );
}
