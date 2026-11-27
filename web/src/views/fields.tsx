import type { ReactNode } from "react";
import type { FieldHelp } from "../lib/help";
import type { Option } from "../lib/meta";

/**
 * HelpTip is a "?" button that shows text on hover and keyboard focus. The
 * text is always in the DOM and named by aria-describedby, so screen readers
 * read it with the control.
 */
export function HelpTip({ id, label, text }: { id: string; label: string; text: string }) {
  return (
    <span className="tip">
      <button type="button" className="tip-btn" aria-label={`About ${label}`} aria-describedby={id}>?</button>
      <span role="tooltip" id={id} className="tip-text">{text}</span>
    </span>
  );
}

function Frame({ id, help, children }: { id: string; help: FieldHelp; children: ReactNode }) {
  return (
    <div className="field">
      <div className="field-head">
        <label htmlFor={id}>{help.label}</label>
        <HelpTip id={`${id}-help`} label={help.label} text={help.help} />
      </div>
      {children}
    </div>
  );
}

interface SelectProps {
  id: string;
  label?: string;
  help: FieldHelp;
  value: string;
  options: Option[];
  /** allLabel names the empty choice; null leaves it out for a control that always has a value. */
  allLabel?: string | null;
  onChange: (v: string) => void;
}

/** SelectField is a labelled dropdown with an "All" choice for a finite set of values. */
export function SelectField({ id, label, help, value, options, allLabel = "All", onChange }: SelectProps) {
  const h = label ? { ...help, label } : help;
  return (
    <Frame id={id} help={h}>
      <select id={id} value={value} aria-describedby={`${id}-help`} onChange={(e) => onChange(e.currentTarget.value)}>
        {allLabel !== null && <option value="">{allLabel}</option>}
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </Frame>
  );
}

/** TextField is for open values such as ids, where no finite list exists. */
export function TextField({ id, help, value, placeholder, inputMode, onChange }: { id: string; help: FieldHelp; value: string; placeholder?: string; inputMode?: "numeric" | "text"; onChange: (v: string) => void }) {
  return (
    <Frame id={id} help={help}>
      <input id={id} type="text" value={value} placeholder={placeholder} inputMode={inputMode} aria-describedby={`${id}-help`} onChange={(e) => onChange(e.currentTarget.value)} />
    </Frame>
  );
}

/** Blurb is the one-line description under a view's title. */
export function Blurb({ text }: { text: string }) {
  return <p className="blurb">{text}</p>;
}
