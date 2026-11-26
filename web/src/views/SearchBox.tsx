import { useState } from "preact/hooks";
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
  const submit = (e: Event) => {
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
    <form class="search" role="search" aria-label="Search the chain" onSubmit={submit}>
      <label for="search-q" class="sr">Search</label>
      <input
        id="search-q"
        type="search"
        value={q}
        placeholder="Block, tx, address, pool, actor"
        aria-describedby="search-tip"
        maxLength={200}
        onInput={(e) => setQ(e.currentTarget.value)}
      />
      <button type="submit">Search</button>
      <HelpTip id="search-tip" label={FIELD_HELP.search!.label} text={FIELD_HELP.search!.help} />
      {msg && <span class="search-msg" role="alert">{msg}</span>}
    </form>
  );
}
