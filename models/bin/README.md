# models/bin

Wrappers around `tla2tools.jar`. `_java` is shared: it uses the Java and jar
that `make models-install` placed in `models/.tools`, falling back to `java`
on `PATH` where no JDK is pinned for the platform (`TLA2TOOLS_JAR` overrides
the jar). It caps the heap at `TLC_HEAP` (default `6g`);
`TLC_REQUIRE_PINNED_JAVA=1` refuses the fallback. `_java --check` prints the
toolchain a run uses, or exits 2 when it is missing.

- `sany Spec.tla`: the parser; checks syntax and names.
- `tlc -config Spec.cfg Spec.tla`: the model checker. Run directly, it writes
  `states/` into the current directory; `scripts/run-model.sh` gives each
  run a private directory instead.
