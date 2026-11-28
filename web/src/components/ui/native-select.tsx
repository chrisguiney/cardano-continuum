import type { ComponentProps } from "react";
import { ChevronDownIcon } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * NativeSelect is shadcn/ui's styled <select>. It keeps the browser's own
 * listbox, so keyboard use, form semantics and an empty-string "All" value
 * work as they always have.
 */
function NativeSelect({ className, ...props }: ComponentProps<"select">) {
  return (
    <div className="group/native-select relative w-fit has-[select:disabled]:opacity-50" data-slot="native-select-wrapper">
      <select
        data-slot="native-select"
        className={cn(
          "h-9 w-full min-w-0 appearance-none rounded-md border border-input bg-card py-1 pr-8 pl-3 text-sm shadow-xs transition-[color,box-shadow] outline-none disabled:pointer-events-none disabled:cursor-not-allowed",
          "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
          className,
        )}
        {...props}
      />
      <ChevronDownIcon className="pointer-events-none absolute top-1/2 right-2.5 size-4 -translate-y-1/2 text-muted-foreground select-none" aria-hidden="true" />
    </div>
  );
}

export { NativeSelect };
