import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FIELD_HELP } from "../lib/help";
import { runSearch } from "../lib/search";
import { navigate } from "../lib/router";
import { HelpTip } from "./fields";

/**
 * SearchBox routes a block hash or height, slot, transaction hash, address,
 * pool id or actor id to the view that shows it.
 */
export function SearchBox() {
  const [q, setQ] = useState("");
  const [msg, setMsg] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void runSearch(q).then((r) => {
      if (r.ok) {
        setMsg("");
        setQ("");
        navigate(r.route);
      } else {
        setMsg(r.message);
      }
    });
  };
  return (
    <form className="flex flex-wrap items-center gap-1.5" role="search" aria-label="Search the chain" onSubmit={submit}>
      <label htmlFor="search-q" className="sr-only">Search</label>
      <Input
        id="search-q"
        type="search"
        value={q}
        placeholder="Block, tx, address, pool, actor"
        aria-describedby="search-tip"
        maxLength={200}
        className="h-8 w-60 max-w-full"
        onChange={(e) => setQ(e.currentTarget.value)}
      />
      <Button type="submit" variant="outline" size="sm">Search</Button>
      <HelpTip id="search-tip" label={FIELD_HELP.search!.label} text={FIELD_HELP.search!.help} />
      {msg && <span className="basis-full text-xs text-destructive" role="alert">{msg}</span>}
    </form>
  );
}
