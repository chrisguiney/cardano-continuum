# models/specs/hello

A toy keep-alive protocol (`Hello.tla`): the client sends "ping", the server
answers "pong", and the client may say "done" on its turn. `Hello.cfg` must
pass; `HelloBug.cfg` is a mutation that must fail. Run with
`models/scripts/run-model.sh specs/hello` from `models/`, or `make models`.
