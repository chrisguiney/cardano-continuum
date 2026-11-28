import { NativeSelect } from "@/components/ui/native-select";
import { FIELD_HELP } from "../lib/help";
import { parseStyle, STYLES, STYLE_LABEL, type Style } from "../lib/appearance";
import { HelpTip } from "./fields";

/** StyleSelect picks the palette family beside the light/dark button. */
export function StyleSelect({ style, onChange }: { style: Style; onChange: (s: Style) => void }) {
  return (
    <span className="inline-flex items-center">
      <NativeSelect aria-label="Style" aria-describedby="style-help" value={style} onChange={(e) => onChange(parseStyle(e.target.value))}>
        {STYLES.map((s) => (
          <option key={s} value={s}>
            {STYLE_LABEL[s]}
          </option>
        ))}
      </NativeSelect>
      <HelpTip id="style-help" label="Style" text={FIELD_HELP.style!.help} />
    </span>
  );
}
