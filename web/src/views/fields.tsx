import type { ReactNode } from "react";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { FieldHelp } from "../lib/help";
import type { Option } from "../lib/meta";

/**
 * HelpTip is a "?" button whose text shows in a tooltip on hover and on
 * keyboard focus. The text is also always in the DOM, visually hidden and
 * named by the button's aria-describedby, so a screen reader reads it with
 * the control whether or not the tooltip is open. The explicit
 * aria-describedby overrides the one Radix sets only while it is open.
 */
export function HelpTip({ id, label, text }: { id: string; label: string; text: string }) {
  return (
    <span className="inline-flex align-middle">
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={`About ${label}`}
            aria-describedby={id}
            className="ml-1 inline-grid size-[1.15rem] cursor-help place-items-center rounded-full border border-primary text-[0.7rem] leading-none font-bold text-primary normal-case outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            ?
          </button>
        </TooltipTrigger>
        <TooltipContent>{text}</TooltipContent>
      </Tooltip>
      <span id={id} className="sr-only">{text}</span>
    </span>
  );
}

function Frame({ id, help, children }: { id: string; help: FieldHelp; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center">
        <label htmlFor={id} className="text-[0.85rem] text-muted-foreground">{help.label}</label>
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
      <NativeSelect id={id} value={value} aria-describedby={`${id}-help`} className="min-w-32" onChange={(e) => onChange(e.currentTarget.value)}>
        {allLabel !== null && <option value="">{allLabel}</option>}
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </NativeSelect>
    </Frame>
  );
}

/** TextField is for open values such as ids, where no finite list exists. */
export function TextField({ id, help, value, placeholder, inputMode, onChange }: { id: string; help: FieldHelp; value: string; placeholder?: string; inputMode?: "numeric" | "text"; onChange: (v: string) => void }) {
  return (
    <Frame id={id} help={help}>
      <Input id={id} type="text" value={value} placeholder={placeholder} inputMode={inputMode} aria-describedby={`${id}-help`} className="w-40" onChange={(e) => onChange(e.currentTarget.value)} />
    </Frame>
  );
}

/** Blurb is the one-line description under a view's title. */
export function Blurb({ text }: { text: string }) {
  return <p className="-mt-2 mb-4 text-muted-foreground">{text}</p>;
}
