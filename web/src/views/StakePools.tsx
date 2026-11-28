import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { nodeLink, poolBlocksLink, poolLink } from "../lib/explorer";
import { ada, count, short } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { GovernanceView, StakePoolRow, StakePoolsView } from "../lib/types";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Blurb, HelpTip } from "./fields";
import { EmptyRow, ErrorNote, Facts, Heading, Link, Mono, Muted, Note, Panel, Subheading, TableFrame, Title } from "./common";
import { VoteTable } from "./Governance";

/** statusText is a pool's registration state: retired, retiring at an epoch, or registered. */
export function statusText(p: StakePoolRow): string {
  if (p.actor_state === "retired") return "retired";
  if (p.retire_epoch !== null) return `retires at epoch ${p.retire_epoch}`;
  return p.actor_state || "registered";
}

const pct = (m: number) => `${(m * 100).toFixed(m * 100 < 10 ? 1 : 0)}%`;

/** NodesCell lists the nodes running a pool's keys, or says the pool exists only on chain. */
export function NodesCell({ p }: { p: StakePoolRow }) {
  if (p.nodes.length === 0) return <Muted title="no node runs this pool's keys: it exists only as certificates on chain">no node (logical)</Muted>;
  return (
    <span className="flex flex-wrap gap-x-2">
      {p.nodes.map((n) => <Link key={n.name} to={nodeLink(n.name)}>{n.name} <Muted>({n.impl})</Muted></Link>)}
    </span>
  );
}

function Operator({ p }: { p: StakePoolRow }) {
  if (p.actor === null) return <Muted>no actor</Muted>;
  return <Link to={`/actors/${p.actor}`}>{p.actor_name || `actor ${p.actor}`}</Link>;
}

const NOT_INDEXED = "the server has no explorer index, so the chain's view is unknown";

/** POOL_COLUMNS are the stake pool table's columns, each explained by its FIELD_HELP entry. */
const POOL_COLUMNS: { help: string; num?: boolean }[] = [
  { help: "pool_id" }, { help: "pool_operator" }, { help: "pool_status" },
  { help: "pledge", num: true }, { help: "margin", num: true }, { help: "pool_cost", num: true },
  { help: "pool_delegators", num: true }, { help: "pool_delegated", num: true },
  { help: "pool_blocks", num: true }, { help: "pool_votes", num: true }, { help: "pool_node" },
];

/** StakePoolTable lists the on-chain pools; each row links the pool, its operator and the nodes running it. */
export function StakePoolTable({ rows }: { rows: StakePoolRow[] }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            {POOL_COLUMNS.map((c) => (
              <TableHead key={c.help} className={c.num ? "text-right" : undefined}>
                {FIELD_HELP[c.help]!.label}
                <HelpTip id={`ph-${c.help}`} label={FIELD_HELP[c.help]!.label} text={FIELD_HELP[c.help]!.help} />
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((p) => (
            <TableRow key={p.id}>
              <TableCell><Link to={poolLink(p.id)}><Mono title={p.bech32}>{short(p.bech32 || p.id, 12)}</Mono></Link></TableCell>
              <TableCell><Operator p={p} /></TableCell>
              <TableCell>{statusText(p)}</TableCell>
              <TableCell className="text-right tabular-nums">{p.registration ? ada(p.registration.pledge) : <Muted title="no registration certificate seen: a genesis pool, or no explorer">–</Muted>}</TableCell>
              <TableCell className="text-right tabular-nums">{p.registration ? pct(p.registration.margin) : "–"}</TableCell>
              <TableCell className="text-right tabular-nums">{p.registration ? ada(p.registration.cost) : "–"}</TableCell>
              <TableCell className="text-right tabular-nums">{count(p.delegators)}</TableCell>
              <TableCell className="text-right tabular-nums">{ada(p.delegated_lovelace)}</TableCell>
              <TableCell className="text-right tabular-nums">{p.blocks === null ? <Muted title={NOT_INDEXED}>–</Muted> : <Link to={poolBlocksLink(p.id)}>{count(p.blocks)}</Link>}</TableCell>
              <TableCell className="text-right tabular-nums">{count(p.votes)}</TableCell>
              <TableCell><NodesCell p={p} /></TableCell>
            </TableRow>
          ))}
          {rows.length === 0 && <EmptyRow cols={11}>No stake pools yet.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

/** StakePoolDetail is one stake pool in full. */
export function StakePoolDetail({ p }: { p: StakePoolRow }) {
  const r = p.registration;
  return (
    <Panel>
      <Heading>Stake pool <Mono className="font-normal">{short(p.bech32 || p.id, 16)}</Mono></Heading>
      <Facts rows={[
        ["Pool id (bech32)", <Mono>{p.bech32}</Mono>],
        ["Pool id (hex)", <Mono>{p.id}</Mono>],
        ["Operator", <><Operator p={p} />{p.profile && <Muted> {p.profile}</Muted>}</>],
        ["Status", statusText(p)],
        ["Pledge", r ? ada(r.pledge) : <Muted>no registration certificate seen (a genesis pool, or no explorer)</Muted>],
        ["Margin", r ? pct(r.margin) : "–"],
        ["Fixed cost", r ? ada(r.cost) : "–"],
        ["Owners", r ? count(r.owners) : "–"],
        ["Metadata", r?.metadata_url ? <Mono>{r.metadata_url}</Mono> : <Muted>none (no ticker or name)</Muted>],
        ["Delegators", count(p.delegators)],
        ["Delegated", <>{ada(p.delegated_lovelace)} <Muted>(the delegators' settled funds in the engine's ledger, not the ledger's stake snapshot)</Muted></>],
        ["Blocks issued", p.blocks === null ? <Muted>{NOT_INDEXED}</Muted> : <Link to={poolBlocksLink(p.id)}>{count(p.blocks)}</Link>],
        ["Governance votes", count(p.votes)],
        ["Nodes running its keys", <NodesCell p={p} />],
      ]} />
    </Panel>
  );
}

/** SPOVotes lists each pool actor's recent votes, from the engine's chain view. */
function SPOVotes({ actor }: { actor?: number | null }) {
  const { data: g, error } = usePoll<GovernanceView>("/api/governance", 5000);
  const pools = g?.pools.filter((p) => actor === undefined || p.actor === actor) ?? [];
  return (
    <Panel>
      <Heading>Recent SPO votes</Heading>
      <ErrorNote error={error} />
      {g && pools.length === 0 && <Note className="p-4 text-center">No vote yet.</Note>}
      {pools.map((p) => (
        <div key={p.actor}>
          <Subheading><Link to={`/actors/${p.actor}`}>pool actor {p.actor}</Link>{p.profile && <Muted className="font-normal"> {p.profile}</Muted>}</Subheading>
          <VoteTable votes={p.votes} empty="No votes." />
        </div>
      ))}
    </Panel>
  );
}

/** StakePools is the on-chain stake pools view; with id it opens on that pool. */
export function StakePools({ id = "" }: { id?: string }) {
  const { data: v, error } = usePoll<StakePoolsView>("/api/pools", 5000);
  const one = id ? v?.pools.find((p) => p.id === id) : undefined;
  return (
    <>
      <Title>{id ? "Stake pool" : "Stake pools"}</Title>
      <Blurb text={VIEW_BLURB.pools!} />
      <ErrorNote error={error} />
      {!v && !error && <Note>Loading…</Note>}
      {v && id && (one ? <StakePoolDetail p={one} /> : <Note className="p-4 text-center">No stake pool <Mono>{id}</Mono> is known.</Note>)}
      {v && id && <Note><Link to="/pools">All stake pools</Link></Note>}
      {v && !id && <StakePoolTable rows={v.pools} />}
      {v && (!id || one) && <SPOVotes actor={one ? one.actor : undefined} />}
    </>
  );
}
