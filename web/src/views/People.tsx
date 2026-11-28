import { ada, count, simTime } from "../lib/format";
import { VIEW_BLURB } from "../lib/help";
import { HUMAN_HELP } from "../lib/humanhelp";
import { usePoll } from "../lib/hooks";
import { money, savingsRate, walletCounts } from "../lib/people";
import type { PeopleView, Person as PersonData } from "../lib/types";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, ErrorNote, Facts, Grid, Heading, Link, Mono, Note, Panel, Stat, Stats, TableFrame, Title } from "./common";
import { Blurb, HelpTip } from "./fields";

/** Th is a column heading with its help. */
function Th({ k, num }: { k: string; num?: boolean }) {
  const h = HUMAN_HELP[k]!;
  return <TableHead className={num ? "text-right" : undefined}>{h.label} <HelpTip id={`ph-${k}`} label={h.label} text={h.help} /></TableHead>;
}

/** Label is a fact's name with its help. */
function Label({ k }: { k: string }) {
  const h = HUMAN_HELP[k]!;
  return <>{h.label} <HelpTip id={`pf-${k}`} label={h.label} text={h.help} /></>;
}

const pct = (x: number) => `${(100 * x).toFixed(0)}%`;

/** People lists the humans who own the wallets, or one of them when id is given. */
export function People({ id }: { id?: number }) {
  return id === undefined ? <List /> : <One id={id} />;
}

function List() {
  const { data, error } = usePoll<PeopleView>("/api/people", 5000);
  const rows = data?.humans ?? [];
  const wallets = rows.reduce((n, r) => n + r.wallets, 0);
  const invested = rows.reduce((n, r) => n + r.purchases, 0);
  return (
    <>
      <Title>People</Title>
      <Blurb text={VIEW_BLURB.people!} />
      <ErrorNote error={error} />
      {data && rows.length === 0 && <Note>This run has no humans: its profile file has no humans section, so each wallet is its own owner.</Note>}
      <Stats>
        <Stat label="Humans" value={count(rows.length)} sub={`${count(wallets)} wallets`} help={HUMAN_HELP.human!.help} />
        <Stat label="Wallets per human" value={rows.length ? (wallets / rows.length).toFixed(2) : "–"} help={HUMAN_HELP.wallets!.help} />
        <Stat label="Out of work" value={data ? pct(data.unemployed) : "–"} sub="at their last payday" help={HUMAN_HELP.employed!.help} />
        <Stat label="Purchases from income" value={count(invested)} help={HUMAN_HELP.invested!.help} />
      </Stats>

      <Heading>How many wallets each human keeps</Heading>
      <TableFrame>
        <Table>
          <TableHeader><TableRow><Th k="wallets" /><TableHead className="text-right">Humans</TableHead></TableRow></TableHeader>
          <TableBody>
            {walletCounts(rows).map(([n, h]) => (
              <TableRow key={n}><TableCell>{n}</TableCell><TableCell className="text-right tabular-nums">{count(h)}</TableCell></TableRow>
            ))}
            {rows.length === 0 && <EmptyRow cols={2}>No humans.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>

      <Heading>Humans</Heading>
      <TableFrame tabIndex={0} role="region" aria-label="Humans, scrolls sideways">
        <Table aria-label="Humans">
          <TableHeader>
            <TableRow>
              <Th k="human" /><Th k="motivation" /><Th k="wealth_tier" /><Th k="employed" /><Th k="income" num /><Th k="bills" num />
              <Th k="fiat" num /><Th k="share" num /><Th k="style" /><Th k="wallets" num /><Th k="lovelace" num />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.id} className={r.gone ? "text-muted-foreground" : undefined}>
                <TableCell><Link to={`/people/${r.id}`}>{r.id}</Link>{r.gone ? " (gone)" : ""}</TableCell>
                <TableCell>{r.motivation}</TableCell>
                <TableCell>{r.tier}</TableCell>
                <TableCell>{r.employed ? "yes" : "no"}</TableCell>
                <TableCell className="text-right tabular-nums">{money(r.income, r.currency)}</TableCell>
                <TableCell className="text-right tabular-nums">{money(r.bills, r.currency)}</TableCell>
                <TableCell className="text-right tabular-nums">{money(r.fiat, r.currency)}</TableCell>
                <TableCell className="text-right tabular-nums">{pct(r.share)}</TableCell>
                <TableCell>{r.style ?? ""}</TableCell>
                <TableCell className="text-right tabular-nums">{count(r.wallets)}</TableCell>
                <TableCell className="text-right tabular-nums">{ada(r.lovelace)}</TableCell>
              </TableRow>
            ))}
            {rows.length === 0 && <EmptyRow cols={11}>No humans.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>
    </>
  );
}

function One({ id }: { id: number }) {
  const { data: p, error } = usePoll<PersonData>(`/api/people/${id}`, 5000);
  return (
    <>
      <p className="mt-4"><Link to="/people">← People</Link></p>
      <Title>Human {id}{p ? ` · ${p.motivation}, ${p.tier} tier` : ""}</Title>
      <Blurb text={VIEW_BLURB.person!} />
      <ErrorNote error={error} />
      {p && <Detail p={p} />}
    </>
  );
}

function Detail({ p }: { p: PersonData }) {
  const c = p.currency;
  return (
    <>
      <Grid>
        <Panel aria-labelledby="h-household">
          <Heading id="h-household">Household</Heading>
          <Facts muted rows={[
            [<Label k="employed" />, p.employed ? "yes" : "no"],
            [<Label k="income" />, money(p.income, c)],
            [<Label k="bills" />, `${money(p.bills, c)} (${pct(p.income ? p.bills / p.income : 0)} of pay)`],
            ["Left to save", pct(savingsRate(p))],
            [<Label k="fiat" />, `${money(p.fiat, c)}, started with ${money(p.fiat0, c)}`],
            [<Label k="held" />, money(p.held, c)],
          ]} />
        </Panel>
        <Panel aria-labelledby="h-strategy">
          <Heading id="h-strategy">Investment strategy</Heading>
          <Facts muted rows={[
            [<Label k="share" />, `${pct(p.share)} of disposable income`],
            [<Label k="style" />, `${p.style ?? "none"}, ${p.steps} purchases a month`],
            [<Label k="buffer" />, `${p.buffer} months of bills`],
            [<Label k="spread" />, p.spread.toFixed(2)],
            [<Label k="invested" />, `${money(p.invested, c)} over ${count(p.purchases)} purchases`],
          ]} />
        </Panel>
        <Panel aria-labelledby="h-months">
          <Heading id="h-months">So far</Heading>
          <Facts muted rows={[
            ["Paydays", `${count(p.months)}, ${count(p.out)} out of work`],
            ["Months short of bills", count(p.short)],
            ["Pay received", money(p.earned, c)],
            ["Bills paid", money(p.paid, c)],
            ["ADA bought, sold", `${money(p.bought, c)}, ${money(p.sold, c)}`],
          ]} />
        </Panel>
      </Grid>

      <Heading>Wallets</Heading>
      <TableFrame>
        <Table aria-label="Wallets">
          <TableHeader>
            <TableRow>
              <TableHead>Wallet</TableHead><Th k="purpose" /><TableHead>Profile</TableHead><TableHead>State</TableHead>
              <Th k="lovelace" num /><TableHead>Pool</TableHead><TableHead>DRep</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {p.wallet_list.map((w) => (
              <TableRow key={w.id}>
                <TableCell><Link to={`/actors/${w.id}`}>{w.id}</Link></TableCell>
                <TableCell>{w.purpose}</TableCell>
                <TableCell>{w.profile ?? ""}</TableCell>
                <TableCell>{w.state ?? ""}</TableCell>
                <TableCell className="text-right tabular-nums">{ada(w.lovelace)}</TableCell>
                <TableCell><Mono>{w.pool ? `${w.pool.slice(0, 12)}…` : "none"}</Mono></TableCell>
                <TableCell><Mono>{w.drep ? `${w.drep.slice(0, 12)}…` : "none"}</Mono></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableFrame>

      <Heading className="mt-6">Money timeline</Heading>
      <ol className="rounded-lg border bg-card">
        {[...p.events].reverse().map((e) => (
          <li key={e.seq} className="border-b px-3 py-2 last:border-b-0">
            <Mono className="mr-2 text-muted-foreground">{simTime(e.at)}</Mono>
            <span className="mr-2 font-semibold">{e.kind}</span>
            {e.cents !== 0 && <span className="mr-2 tabular-nums">{money(e.cents, c)}</span>}
            <span>{e.detail}</span>
            {e.wallet ? <> into <Link to={`/actors/${e.wallet}`}>wallet {e.wallet}</Link></> : null}
          </li>
        ))}
        {p.events.length === 0 && <li className="p-4 text-center text-muted-foreground">No money events yet.</li>}
      </ol>
    </>
  );
}
