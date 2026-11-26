import type { Finding } from "./types";

export interface Badge {
  text: string;
  title: string;
  tone: "warn";
}

// reproBadge flags a finding whose node ran a dirty build: the image held
// uncommitted source, so its replay command cannot rebuild what ran.
export function reproBadge(f: Pick<Finding, "reproducible">): Badge | null {
  if (f.reproducible) return null;
  return {
    text: "not reproducible",
    title: "A node ran a build with uncommitted source; its commit does not reproduce the image.",
    tone: "warn",
  };
}
