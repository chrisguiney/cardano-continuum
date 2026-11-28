import { useEffect, useState } from "react";
import { findingsPath, groupKnown, knownSummary, reproBadge, type FindingFilters } from "../lib/findings";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Finding, FindingPage } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, ErrorNote, Filters, Heading, Note, Panel, TableFrame, Title } from "./common";
import { Blurb } from "./fields";
import { FindingFilterBar } from "./filterbars";

function badgeOf(f: Finding) {
  const b = reproBadge(f);
  return b ? <Badge variant={b.tone} title={b.title}>{b.text}</Badge> : "yes";
}

function Row({ f }: { f: Finding }) {
  return (
    <TableRow>
      <TableCell>{f.id}</TableCell>
      <TableCell><Badge variant={f.severity === "fatal" ? "bad" : f.severity === "error" ? "warn" : "default"}>{f.severity}</Badge></TableCell>
      <TableCell>{f.kind}</TableCell>
      <TableCell className="min-w-64 whitespace-normal">{f.summary}</TableCell>
      <TableCell className="min-w-40 whitespace-normal">{f.verdicts.map((v) => `${v.impl}: ${v.verdict}`).join("; ")}</TableCell>
      <TableCell>{f.provenance ?? ""}</TableCell>
      <TableCell>{badgeOf(f)}</TableCell>
      <TableCell>{f.replay_command ? <code className="text-[0.85em]">{f.replay_command}</code> : ""}</TableCell>
    </TableRow>
  );
}

function Head() {
  return (
    <TableRow>
      <TableHead>Id</TableHead><TableHead>Severity</TableHead><TableHead>Kind</TableHead><TableHead>Summary</TableHead>
      <TableHead>Verdicts</TableHead><TableHead>Build</TableHead><TableHead>Reproducible</TableHead><TableHead>Replay</TableHead>
    </TableRow>
  );
}

const FILTER_DEFAULTS: FindingFilters = { known: "false", kind: "", severity: "" };

// Known findings are explained by an upstream issue. They stay recorded and
// are listed on request, grouped by issue, so new findings are not buried.
export function Findings() {
  const [showKnown, setShowKnown] = useState(false);
  const [filt, setFilt] = useState<FindingFilters>(() => parseQuery(location.search, FILTER_DEFAULTS));
  const enums = useEnums();
  useEffect(() => writeQuery(buildQuery({ ...filt }, FILTER_DEFAULTS)), [filt]);
  const fresh = usePoll<FindingPage>(findingsPath(filt), 10000);
  const known = usePoll<FindingPage>(showKnown ? "/api/findings?limit=100&known=true" : "/api/findings?limit=1&known=true", 10000);
  const data = fresh.data;
  const knownTotal = data?.known_total ?? 0;
  return (
    <>
      <Title>Findings</Title>
      <Blurb text={VIEW_BLURB.findings!} />
      <Filters aria-label="Filter findings">
        <FindingFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
      </Filters>
      <ErrorNote error={fresh.error} />
      <TableFrame>
        <Table>
          <TableHeader><Head /></TableHeader>
          <TableBody>
            {data?.rows.map((f) => <Row key={f.id} f={f} />)}
            {data && data.rows.length === 0 && <EmptyRow cols={8}>No findings match. Oracle disagreements will appear here.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>
      {data && <Note role="status">{count(data.total)} findings shown</Note>}
      {knownTotal > 0 && filt.known === "false" && (
        <Panel>
          <Heading>Known issues</Heading>
          <Note>
            {count(knownTotal)} findings are explained by an upstream issue ({knownSummary(data?.known_counts)}).{" "}
            <Button type="button" variant="outline" size="sm" onClick={() => setShowKnown(!showKnown)}>{showKnown ? "Hide" : "Show"}</Button>
          </Note>
          {showKnown && groupKnown(known.data?.rows ?? []).map((g) => (
            <details key={g.ref} className="my-1">
              <summary className="cursor-pointer text-primary">{g.ref} ({count(data?.known_counts?.[g.ref] ?? g.rows.length)} findings)</summary>
              <TableFrame>
                <Table>
                  <TableHeader><Head /></TableHeader>
                  <TableBody>{g.rows.map((f) => <Row key={f.id} f={f} />)}</TableBody>
                </Table>
              </TableFrame>
            </details>
          ))}
        </Panel>
      )}
    </>
  );
}
