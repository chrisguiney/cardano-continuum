# models/bin

Wrappers around `tla2tools.jar`. `_java` is shared: it uses the Java and jar
that `make models-install` placed in `models/.tools`, falling back to `java`
on `PATH` (`TLA2TOOLS_JAR` overrides the jar).

- `sany Spec.tla`: the parser; checks syntax and names.
- `tlc -config Spec.cfg Spec.tla`: the model checker.
