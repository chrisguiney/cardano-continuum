--------------------------- MODULE EngineRecovery ---------------------------
(***************************************************************************)
(* The continuum engine across crashes, restarts and rollbacks.            *)
(*                                                                         *)
(* A node grows a chain and may roll it back by at most K blocks from the  *)
(* longest chain it has had. The engine follows that chain with           *)
(* chain-sync, derives actor state from it, and persists two things: an   *)
(* append-only event log of every input it consumed (blocks, rollbacks,   *)
(* clock ticks), and a checkpoint (its chain points, its actor state and  *)
(* the log sequence number the checkpoint reflects). It can crash at any  *)
(* step, losing everything in memory. On restart it loads the checkpoint, *)
(* replays the log tail written after it, and asks the node for the       *)
(* latest point both still share (FindIntersect), which can be older than *)
(* the stored cursor if the node rolled back while the engine was down.   *)
(*                                                                         *)
(* Two designs share the module (constant Variant):                        *)
(*   "Stable" (design variant A): actor state is derived only from blocks *)
(*     at least K deep in the followed chain. A rollback never reaches it, *)
(*     so no undo information is kept; the tip is a volatile overlay.     *)
(*   "Undo" (design variant B): actor state is derived from every block,  *)
(*     including the tip, and the last K blocks carry undo information so *)
(*     a rollback can unwind them, also after a restart.                  *)
(*                                                                         *)
(* Actors are abstracted to one counter each: a block names one actor and *)
(* bumps its counter. Derive is the reference fold over blocks.           *)
(*                                                                         *)
(* Sources: plans/FORMAL-METHODS.md, model engine-recovery;              *)
(* plans/IMPLEMENTATION.md 2.6 (chain view), 2.12 (event log and         *)
(* replay), 2.13 (store), 2.22 (engine loop). The Go code does not exist  *)
(* yet; this is a design model.                                           *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see the "Reading a formula literally" table  *)
(* in models/specs/engine-recovery/README.md.                             *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    Actors,        \* the actors a block can name
    K,             \* the security parameter: the node never rolls back more
                   \* than K blocks below the longest chain it has had
    MaxLen,        \* the longest the node's chain gets (bound)
    MaxRollbacks,  \* how many times the node rolls back (bound)
    MaxCrashes,    \* how many times the engine crashes (bound)
    MaxTicks,      \* how many clock ticks the engine consumes (bound)
    MaxBatch,      \* how many log entries the store writer batches (bound)
    Variant,       \* "Stable" (design A) or "Undo" (design B)
    Mutation       \* "none", or one deliberate mistake (see the README)

VARIABLES
    \* The node.
    chain,    \* the node's current chain: a sequence of blocks
    imm,      \* the node's immutable height: blocks 1..imm never change
    rb,       \* how many rollbacks the node has made
    \* The engine's memory (lost on a crash).
    up,       \* is the engine running?
    crashes,  \* how many times it has crashed
    st,       \* its state: points followed, confirmed height, actors, undo
    pend,     \* log entries consumed but not yet durable
    srv,      \* the node's chain-sync read pointer for this client
    half,     \* SplitCommit only: the checkpoint's first half is written
    halted,   \* it met a rollback it cannot unwind
    \* The durable store (survives a crash).
    slog,     \* the event log: every input entry made durable, in order
    ck        \* the checkpoint: [seq, st], seq = log entries it reflects

\* In English: "The variables grouped by who owns them: the node, the
\* engine's memory, the durable store, and all of them."
\* Literally: "node is defined as the tuple of chain, imm and rb."
node   == <<chain, imm, rb>>
\* Literally: "engine is defined as the tuple of up, crashes, st, pend, srv,
\*   half and halted."
engine == <<up, crashes, st, pend, srv, half, halted>>
\* Literally: "store is defined as the tuple of slog and ck."
store  == <<slog, ck>>
\* Literally: "vars is defined as the tuple of node, engine and store."
vars   == <<node, engine, store>>

-----------------------------------------------------------------------------
\* Helpers.

\* In English: "The smaller of a and b."
\* Literally: "Min of a and b is defined as: if a is less than b then a,
\*   else b."
Min(a, b) == IF a < b THEN a ELSE b

\* In English: "The larger of a and b."
\* Literally: "Max of a and b is defined as: if a is greater than b then a,
\*   else b."
Max(a, b) == IF a > b THEN a ELSE b

\* In English: "A block names one actor and the number of node rollbacks
\* made before it was forged. Two blocks at one height on different forks
\* therefore differ, which is what an intersection compares."
\* Literally: "Blocks is defined as the set of records whose field a is a
\*   member of Actors and whose field g is a member of 0 through
\*   MaxRollbacks."
Blocks == [a : Actors, g : 0..MaxRollbacks]

\* In English: "The placeholder in a log entry that carries no block."
\* Literally: "NoBlock is defined as the record whose field a is "none" and
\*   whose field g is 0."
NoBlock == [a |-> "none", g |-> 0]

\* In English: "The three inputs the engine logs: a block rolled forward, a
\* rollback to height n, and a clock tick."
\* Literally: "Fwd of b is defined as the record whose field k is "fwd",
\*   field b is b, and field n is 0."
Fwd(b) == [k |-> "fwd", b |-> b, n |-> 0]
\* Literally: "Back of n is defined as the record whose field k is "back",
\*   field b is NoBlock, and field n is n."
Back(n) == [k |-> "back", b |-> NoBlock, n |-> n]
\* Literally: "TickIn is defined as the record whose field k is "tick",
\*   field b is NoBlock, and field n is 0."
TickIn == [k |-> "tick", b |-> NoBlock, n |-> 0]

\* In English: "s is a sequence of at most n members of S."
\* Literally: "IsSeqOf of s, S and n is defined as: the length of s is at
\*   most n, and for all i in 1 through the length of s, element i of s is
\*   a member of S."
IsSeqOf(s, S, n) == Len(s) <= n /\ \A i \in 1..Len(s) : s[i] \in S

\* In English: "s is the start of t."
\* Literally: "IsPrefix of s and t is defined as: the length of s is at most
\*   the length of t, and the first (length of s) elements of t equal s."
IsPrefix(s, t) == Len(s) <= Len(t) /\ SubSeq(t, 1, Len(s)) = s

\* In English: "How many leading blocks s and t share: the height of their
\* latest common point."
\* Literally: "CommonPrefixLen of s and t is defined as: letting m be Min of
\*   the lengths of s and t, the n, chosen from 0 through m, such that the
\*   first n elements of s equal the first n elements of t, and n equals m
\*   or element n plus 1 of s does not equal element n plus 1 of t."
CommonPrefixLen(s, t) ==
    LET m == Min(Len(s), Len(t))
    IN  CHOOSE n \in 0..m :
            /\ SubSeq(s, 1, n) = SubSeq(t, 1, n)
            /\ (n = m \/ s[n + 1] /= t[n + 1])

\* In English: "The reference: each actor's counter is the number of blocks
\* in seq that name it."
\* Literally: "Derive of seq is defined as the function that maps each x in
\*   Actors to the number of members of the set of i in 1 through the length
\*   of seq such that element i of seq's field a equals x."
Derive(seq) == [x \in Actors |-> Cardinality({i \in 1..Len(seq) : seq[i].a = x})]

\* In English: "Every actor's counter at 0."
\* Literally: "Zero is defined as the function that maps each x in Actors to
\*   0."
Zero == [x \in Actors |-> 0]

\* In English: "Count one more effect for actor x."
\* Literally: "Bump of act and x is defined as act with x's counter
\*   increased by 1."
Bump(act, x) == [act EXCEPT ![x] = @ + 1]

\* In English: "Take the counts d away from act, actor by actor."
\* Literally: "Minus of act and d is defined as the function that maps each
\*   x in Actors to act of x minus d of x."
Minus(act, d) == [x \in Actors |-> act[x] - d[x]]

\* In English: "How deep a block must be before the engine counts it. With
\* ConfirmAtDepthZero, design A is mis-implemented and counts the tip."
\* Literally: "Depth is defined as: if Mutation equals "ConfirmAtDepthZero"
\*   then 0, else K."
Depth == IF Mutation = "ConfirmAtDepthZero" THEN 0 ELSE K

-----------------------------------------------------------------------------
\* The engine's state, and the one pure step both the live loop and replay
\* use (the Go Loop.Handle).

\* In English: "An engine state: the chain points it has followed (pts),
\* how many of them its actors reflect (conf), the actor counters, how many
\* top blocks it can unwind (ud, design B), and the ticks consumed."
\* Literally: "Empty is defined as the record whose field pts is the empty
\*   sequence, conf is 0, act is Zero, ud is 0 and ticks is 0."
Empty == [pts |-> <<>>, conf |-> 0, act |-> Zero, ud |-> 0, ticks |-> 0]

\* In English: "The blocks the actor state reflects."
\* Literally: "Confirmed of s is defined as the first s.conf elements of
\*   s.pts."
Confirmed(s) == SubSeq(s.pts, 1, s.conf)

\* In English: "A state is consistent when its actors are exactly what its
\* confirmed blocks imply and it confirms no more blocks than it has."
\* Literally: "Consistent of s is defined as: s.conf is at most the length
\*   of s.pts, and s.act equals Derive of Confirmed of s."
Consistent(s) == s.conf <= Len(s.pts) /\ s.act = Derive(Confirmed(s))

\* In English: "Whether the engine can handle input e from state s. Only a
\* rollback can be impossible: design A cannot roll back below what it has
\* confirmed, design B cannot unwind more blocks than it holds undo for."
\* Literally: "Feasible of s and e is defined as: e's field k is not
\*   "back", or, if Variant equals "Undo", the length of s.pts minus e.n is
\*   at most s.ud, else e.n is at least s.conf."
Feasible(s, e) ==
    \/ e.k /= "back"
    \/ IF Variant = "Undo" THEN Len(s.pts) - e.n <= s.ud
                           ELSE e.n >= s.conf

\* In English: "One deterministic step. A tick bumps the tick count. A
\* block is appended; design B counts it at once and keeps undo for it,
\* design A counts the block that has just become Depth deep. A rollback
\* truncates the points; design B unwinds the counters of the removed
\* blocks, design A keeps its counters (a feasible rollback never reaches
\* them). The step is total so replay of any log is defined; the live loop
\* only takes feasible steps."
\* Literally: "Step of s and e is defined as: in the case e.k equals "tick",
\*   s with ticks increased by 1; in the case e.k equals "fwd" and Variant
\*   equals "Undo", s with e.b appended to pts, conf set to the length of
\*   s.pts plus 1, e.b's actor bumped, and ud set to Min of K and ud plus 1;
\*   in the case e.k equals "fwd", letting v be s.pts with e.b appended: if
\*   the length of v minus Depth is greater than s.conf then s with pts v,
\*   conf increased by 1 and the actor of element s.conf plus 1 of v bumped,
\*   else s with pts v; in the case e.k equals "back" and Variant equals
\*   "Undo", letting n be Min of e.n and the length of s.pts, s with pts cut
\*   to its first n, conf n, act Minus Derive of the removed blocks, and ud
\*   reduced by the number removed, not below 0; in every other case, s with
\*   pts cut to its first n and conf set to Min of conf and n."
Step(s, e) ==
    CASE e.k = "tick" ->
            [s EXCEPT !.ticks = @ + 1]
      [] e.k = "fwd" /\ Variant = "Undo" ->
            [s EXCEPT !.pts = Append(@, e.b), !.conf = Len(s.pts) + 1,
                      !.act = Bump(@, e.b.a), !.ud = Min(K, @ + 1)]
      [] e.k = "fwd" ->
            LET v == Append(s.pts, e.b)
            IN  IF Len(v) - Depth > s.conf
                THEN [s EXCEPT !.pts = v, !.conf = @ + 1,
                               !.act = Bump(@, v[s.conf + 1].a)]
                ELSE [s EXCEPT !.pts = v]
      [] e.k = "back" /\ Variant = "Undo" ->
            LET n == Min(e.n, Len(s.pts))
            IN  [s EXCEPT !.pts = SubSeq(@, 1, n), !.conf = n,
                          !.act = Minus(@, Derive(SubSeq(s.pts, n + 1, Len(s.pts)))),
                          !.ud = Max(0, @ - (Len(s.pts) - n))]
      [] OTHER ->
            LET n == Min(e.n, Len(s.pts))
            IN  [s EXCEPT !.pts = SubSeq(@, 1, n), !.conf = Min(@, n)]

\* In English: "Replay: feed the entries of log to Step, one after another,
\* starting from s."
\* Literally: "Replay of s and log is defined as: if log is empty then s,
\*   else Replay of (Step of s and the head of log) and the tail of log."
RECURSIVE Replay(_, _)
Replay(s, log) == IF log = <<>> THEN s ELSE Replay(Step(s, Head(log)), Tail(log))

\* In English: "The log entries written after the checkpoint."
\* Literally: "LogTail is defined as: if ck.seq is at least the length of
\*   slog then the empty sequence, else the elements of slog from ck.seq
\*   plus 1 to the end."
LogTail == IF ck.seq >= Len(slog) THEN <<>> ELSE SubSeq(slog, ck.seq + 1, Len(slog))

\* In English: "What a restart recovers from the store: the checkpoint with
\* the log tail replayed on top."
\* Literally: "Recover is defined as Replay of ck.st and LogTail."
Recover == Replay(ck.st, LogTail)

\* In English: "What `continuum replay` computes: the whole log replayed
\* from nothing."
\* Literally: "ReplayAll is defined as Replay of Empty and slog."
ReplayAll == Replay(Empty, slog)

\* In English: "How many top blocks a restarted engine can unwind. With
\* NoUndo the undo window lives only in memory and is gone after a crash."
\* Literally: "DurableUd of s is defined as: if Mutation equals "NoUndo"
\*   then 0, else s.ud."
DurableUd(s) == IF Mutation = "NoUndo" THEN 0 ELSE s.ud

-----------------------------------------------------------------------------
\* Start.

\* In English: "The node has no blocks, the engine runs with nothing, and
\* the store is empty."
\* Literally: "Init is defined as: chain is empty, imm is 0, rb is 0, up is
\*   true, crashes is 0, st is Empty, pend is empty, srv is empty, half is
\*   false, halted is false, slog is empty, and ck is the record whose seq is
\*   0 and st is Empty."
Init ==
    /\ chain = <<>>
    /\ imm = 0
    /\ rb = 0
    /\ up = TRUE
    /\ crashes = 0
    /\ st = Empty
    /\ pend = <<>>
    /\ srv = <<>>
    /\ half = FALSE
    /\ halted = FALSE
    /\ slog = <<>>
    /\ ck = [seq |-> 0, st |-> Empty]

-----------------------------------------------------------------------------
\* The environment: the node, and crashes.

\* In English: "The node forges a block naming some actor. Every block K
\* below its longest chain so far becomes immutable."
\* Literally: "NodeExtend is defined as: the length of chain is less than
\*   MaxLen, and there exists an x in Actors such that chain in the next
\*   state is chain with the block [a |-> x, g |-> rb] appended, and imm in
\*   the next state is Max of imm and the length of chain plus 1 minus K, and
\*   rb, the engine and the store are unchanged."
NodeExtend ==
    /\ Len(chain) < MaxLen
    /\ \E x \in Actors : chain' = Append(chain, [a |-> x, g |-> rb])
    /\ imm' = Max(imm, Len(chain) + 1 - K)
    /\ UNCHANGED <<rb, engine, store>>

\* In English: "The node switches fork: it drops its top blocks, down to
\* some height not below its immutable height."
\* Literally: "NodeRollback is defined as: rb is less than MaxRollbacks, and
\*   there exists an n in imm through the length of chain minus 1 such that
\*   chain in the next state is the first n elements of chain, and rb in the
\*   next state is rb plus 1, and imm, the engine and the store are
\*   unchanged."
NodeRollback ==
    /\ rb < MaxRollbacks
    /\ \E n \in imm..(Len(chain) - 1) : chain' = SubSeq(chain, 1, n)
    /\ rb' = rb + 1
    /\ UNCHANGED <<imm, engine, store>>

\* In English: "The engine dies at any step: memory, the pending log batch
\* and the chain-sync session are lost; the store is kept."
\* Literally: "Crash is defined as: up is true, and crashes is less than
\*   MaxCrashes, and up in the next state is false, crashes in the next
\*   state is crashes plus 1, st in the next state is Empty, pend and srv in
\*   the next state are empty, half in the next state is false, and the
\*   node, halted and the store are unchanged."
Crash ==
    /\ up
    /\ crashes < MaxCrashes
    /\ up' = FALSE
    /\ crashes' = crashes + 1
    /\ st' = Empty
    /\ pend' = <<>>
    /\ srv' = <<>>
    /\ half' = FALSE
    /\ UNCHANGED <<node, halted, store>>

-----------------------------------------------------------------------------
\* The engine.

\* In English: "Chain-sync delivers the next message. The node compares its
\* read pointer with its chain: if the pointer is no longer on the chain it
\* sends a rollback to their latest common point, otherwise the next block.
\* The store writer holds at most MaxBatch unflushed entries (back-pressure).
\* The engine handles the message with Step and queues its log entry; if it
\* cannot handle a rollback, it halts."
\* Literally: "Fetch is defined as: up is true, halted is not true, half is
\*   not true, the length of pend is less than MaxBatch, srv does not equal
\*   chain, and, letting e be Fwd of element
\*   (length of srv plus 1) of chain if srv is a prefix of chain, else Back of
\*   CommonPrefixLen of srv and chain: srv in the next state is srv with e.b
\*   appended if e is a fwd, else the first e.n elements of srv; and if
\*   Feasible of st and e then st in the next state is Step of st and e and
\*   pend in the next state is pend with e appended and halted is unchanged,
\*   else halted in the next state is true and st and pend are unchanged;
\*   and the node, the store, up, crashes and half are unchanged."
Fetch ==
    /\ up /\ ~halted /\ ~half
    /\ Len(pend) < MaxBatch
    /\ srv /= chain
    /\ LET e == IF IsPrefix(srv, chain)
                THEN Fwd(chain[Len(srv) + 1])
                ELSE Back(CommonPrefixLen(srv, chain))
       IN  /\ srv' = IF e.k = "fwd" THEN Append(srv, e.b) ELSE SubSeq(srv, 1, e.n)
           /\ IF Feasible(st, e)
              THEN /\ st' = Step(st, e)
                   /\ pend' = Append(pend, e)
                   /\ UNCHANGED halted
              ELSE /\ halted' = TRUE
                   /\ UNCHANGED <<st, pend>>
    /\ UNCHANGED <<node, store, up, crashes, half>>

\* In English: "The engine reads the clock (a timer fires) and acts on it.
\* The reading is an input and is logged; with UnloggedClock it is not."
\* Literally: "Tick is defined as: up is true, halted is not true, half is
\*   not true, the length of pend is less than MaxBatch, st.ticks is less
\*   than MaxTicks, st in the next state is Step
\*   of st and TickIn, pend in the next state is pend if Mutation equals
\*   "UnloggedClock", else pend with TickIn appended, and the node, the
\*   store, up, crashes, srv, half and halted are unchanged."
Tick ==
    /\ up /\ ~halted /\ ~half
    /\ Len(pend) < MaxBatch
    /\ st.ticks < MaxTicks
    /\ st' = Step(st, TickIn)
    /\ pend' = IF Mutation = "UnloggedClock" THEN pend ELSE Append(pend, TickIn)
    /\ UNCHANGED <<node, store, up, crashes, srv, half, halted>>

\* In English: "The store writer makes the pending log batch durable in one
\* transaction."
\* Literally: "CommitLog is defined as: up is true, half is not true, pend
\*   is not empty, slog in the next state is slog followed by pend, pend in
\*   the next state is empty, and the node, ck, up, crashes, st, srv, half
\*   and halted are unchanged."
CommitLog ==
    /\ up /\ ~half
    /\ pend /= <<>>
    /\ slog' = slog \o pend
    /\ pend' = <<>>
    /\ UNCHANGED <<node, ck, up, crashes, st, srv, half, halted>>

\* In English: "The store writer replaces the checkpoint with the engine's
\* state and the log sequence number it reflects. The correct engine does
\* this only once every input it reflects is in the durable log; with
\* LogAfterEffect it does not wait. With SplitCommit the points and the
\* actor state go in two transactions: the first writes the points and
\* the sequence number and keeps the old actors and ticks."
\* Literally: "CommitCkpt is defined as: up is true, half is not true, pend
\*   is empty or Mutation equals "LogAfterEffect", and, letting new be the
\*   record whose seq is the length of slog plus the length of pend and whose
\*   st is st: new does not equal ck, and if Mutation equals "SplitCommit"
\*   then ck in the next state is new with its st's act and ticks taken from
\*   ck and half in the next state is true, else ck in the next state is new
\*   and half is unchanged; and the node, slog, up, crashes, st, pend, srv
\*   and halted are unchanged."
CommitCkpt ==
    /\ up /\ ~half
    /\ (pend = <<>> \/ Mutation = "LogAfterEffect")
    /\ LET new == [seq |-> Len(slog) + Len(pend), st |-> st]
       IN  /\ new /= ck
           /\ IF Mutation = "SplitCommit"
              THEN /\ ck' = [new EXCEPT !.st.act = ck.st.act, !.st.ticks = ck.st.ticks]
                   /\ half' = TRUE
              ELSE /\ ck' = new
                   /\ UNCHANGED half
    /\ UNCHANGED <<node, slog, up, crashes, st, pend, srv, halted>>

\* In English: "SplitCommit only: the second transaction writes the actor
\* state."
\* Literally: "CommitCkptRest is defined as: up is true, half is true, ck in
\*   the next state is ck with st set to st, half in the next state is false,
\*   and the node, slog, up, crashes, st, pend, srv and halted are
\*   unchanged."
CommitCkptRest ==
    /\ up /\ half
    /\ ck' = [ck EXCEPT !.st = st]
    /\ half' = FALSE
    /\ UNCHANGED <<node, slog, up, crashes, st, pend, srv, halted>>

\* In English: "The engine starts again. It recovers the checkpoint plus the
\* log tail, sends its points to FindIntersect, and the node answers with
\* the latest point both share (ip), which is older than the recovered
\* cursor if the node rolled back while the engine was down. The engine
\* treats that answer as a rollback to ip: it logs it and unwinds, or halts
\* if it cannot. With IntersectAtTip it assumes its stored tip still exists
\* and keeps its state, while the node's read pointer is at ip."
\* Literally: "Restart is defined as: up is not true, and, letting r0 be
\*   Recover, r be r0 with ud set to DurableUd of r0, ip be CommonPrefixLen
\*   of r.pts and chain, and e be Back of ip: up in the next state is true,
\*   srv in the next state is the first ip elements of r.pts, and in the
\*   case ip equals the length of r.pts or Mutation equals "IntersectAtTip",
\*   st in the next state is r, pend in the next state is empty and halted is
\*   unchanged; in the case Feasible of r and e, st in the next state is Step
\*   of r and e, pend in the next state is the sequence of e, and halted is
\*   unchanged; in every other case st in the next state is r, pend is empty
\*   and halted in the next state is true; and the node, the store, crashes
\*   and half are unchanged."
Restart ==
    /\ ~up
    /\ LET r0 == Recover
           r  == [r0 EXCEPT !.ud = DurableUd(r0)]
           ip == CommonPrefixLen(r.pts, chain)
           e  == Back(ip)
       IN  /\ up' = TRUE
           /\ srv' = SubSeq(r.pts, 1, ip)
           /\ CASE ip = Len(r.pts) \/ Mutation = "IntersectAtTip" ->
                      /\ st' = r
                      /\ pend' = <<>>
                      /\ UNCHANGED halted
                [] Feasible(r, e) ->
                      /\ st' = Step(r, e)
                      /\ pend' = <<e>>
                      /\ UNCHANGED halted
                [] OTHER ->
                      /\ st' = r
                      /\ pend' = <<>>
                      /\ halted' = TRUE
    /\ UNCHANGED <<node, store, crashes, half>>

\* In English: "Nothing happens. Keeps TLC from reporting the end of the
\* bounded run as a deadlock; a stuck engine is caught by NeverHalted."
\* Literally: "Idle is defined as: vars is unchanged."
Idle == UNCHANGED vars

\* In English: "A step is a node step, a crash, an engine step, or nothing."
\* Literally: "Next is defined as: NodeExtend, or NodeRollback, or Crash, or
\*   Fetch, or Tick, or CommitLog, or CommitCkpt, or CommitCkptRest, or
\*   Restart, or Idle."
Next ==
    \/ NodeExtend \/ NodeRollback \/ Crash
    \/ Fetch \/ Tick \/ CommitLog \/ CommitCkpt \/ CommitCkptRest \/ Restart
    \/ Idle

\* In English: "The engine's own steps are weakly fair: one that stays
\* possible is eventually taken. The node and crashes get no fairness, and
\* the bounds make them finite."
\* Literally: "Fairness is defined as: weak fairness of Fetch, of
\*   CommitLog, of CommitCkpt, of CommitCkptRest and of Restart."
Fairness ==
    /\ WF_vars(Fetch)
    /\ WF_vars(CommitLog)
    /\ WF_vars(CommitCkpt)
    /\ WF_vars(CommitCkptRest)
    /\ WF_vars(Restart)

\* In English: "Start in Init; every step is a Next step. Safety only."
\* Literally: "Spec is defined as: Init holds at the start, and always every
\*   step is a Next step or leaves vars unchanged."
Spec == Init /\ [][Next]_vars

\* In English: "The same behaviours, with the engine's steps fair."
\* Literally: "LiveSpec is defined as: Spec and Fairness."
LiveSpec == Spec /\ Fairness

-----------------------------------------------------------------------------
\* Invariants.

\* In English: "Every variable has the shape it should."
\* Literally: "TypeOK is defined as: chain is a sequence of at most MaxLen
\*   Blocks; imm is a member of 0 through MaxLen; rb is a member of 0
\*   through MaxRollbacks; up, half and halted are booleans; crashes is a
\*   member of 0 through MaxCrashes; st.pts and ck.st.pts are sequences of at
\*   most MaxLen Blocks; srv is a sequence of at most MaxLen Blocks; st.ticks
\*   is a member of 0 through MaxTicks; and every entry of pend and slog has
\*   field k "fwd", "back" or "tick"."
TypeOK ==
    /\ IsSeqOf(chain, Blocks, MaxLen)
    /\ imm \in 0..MaxLen
    /\ rb \in 0..MaxRollbacks
    /\ up \in BOOLEAN /\ half \in BOOLEAN /\ halted \in BOOLEAN
    /\ crashes \in 0..MaxCrashes
    /\ IsSeqOf(st.pts, Blocks, MaxLen)
    /\ IsSeqOf(ck.st.pts, Blocks, MaxLen)
    /\ IsSeqOf(srv, Blocks, MaxLen)
    /\ st.ticks \in 0..MaxTicks
    /\ \A i \in 1..Len(pend) : pend[i].k \in {"fwd", "back", "tick"}
    /\ \A i \in 1..Len(slog) : slog[i].k \in {"fwd", "back", "tick"}

\* In English: "The engine never meets a rollback it cannot unwind (which
\* would force a full re-sync)."
\* Literally: "NeverHalted is defined as: halted is not true."
NeverHalted == ~halted

\* In English: "Engine and node agree on where the engine is: the engine's
\* points are the node's read pointer for it."
\* Literally: "CursorAgrees is defined as: if up, then st.pts equals srv."
CursorAgrees == up => st.pts = srv

\* In English: "In memory, the actors are what the confirmed blocks imply."
\* Literally: "MemConsistent is defined as: if up, then Consistent of st."
MemConsistent == up => Consistent(st)

\* In English: "The checkpoint never reflects an input the durable log does
\* not hold."
\* Literally: "LogBeforeEffect is defined as: ck.seq is at most the length
\*   of slog."
LogBeforeEffect == ck.seq <= Len(slog)

\* In English: "The checkpoint on disk is consistent on its own: its points
\* and its actors were written together."
\* Literally: "CheckpointConsistent is defined as Consistent of ck.st."
CheckpointConsistent == Consistent(ck.st)

\* In English: "The log alone reproduces what a restart recovers: replaying
\* it from nothing gives the checkpoint plus its tail. Nothing in the store
\* came from an unlogged input."
\* Literally: "ReplayReproduces is defined as: ReplayAll equals Recover."
ReplayReproduces == ReplayAll = Recover

\* In English: "Once the engine has caught up and flushed its log, what a
\* restart would recover is exactly what the chain up to the engine's
\* confirmed height implies, and its points are the chain."
\* Literally: "StoreMatchesChain is defined as: if up, srv equals chain,
\*   st.pts equals chain and pend is empty, then, letting r be Recover, r.pts
\*   equals chain, r.conf is at most the length of chain, and r.act equals
\*   Derive of the first r.conf elements of chain."
StoreMatchesChain ==
    (up /\ srv = chain /\ st.pts = chain /\ pend = <<>>) =>
        LET r == Recover
        IN  /\ r.pts = chain
            /\ r.conf <= Len(chain)
            /\ r.act = Derive(SubSeq(chain, 1, r.conf))

\* In English: "Design A: every block the durable actor state reflects,
\* and every block the running engine's actors reflect, is still on the
\* node's chain, at any moment, before the engine has seen any rollback."
\* Literally: "StableOnly is defined as: Confirmed of Recover is a prefix
\*   of chain, and if up then Confirmed of st is a prefix of chain."
StableOnly ==
    /\ IsPrefix(Confirmed(Recover), chain)
    /\ up => IsPrefix(Confirmed(st), chain)

\* In English: "Design B: if the engine crashed now, the undo information it
\* would recover covers every recovered block the node no longer has."
\* Literally: "UndoCoversWindow is defined as: letting r be Recover, the
\*   length of r.pts minus CommonPrefixLen of r.pts and chain is at most
\*   DurableUd of r."
UndoCoversWindow ==
    LET r == Recover
    IN  Len(r.pts) - CommonPrefixLen(r.pts, chain) <= DurableUd(r)

-----------------------------------------------------------------------------
\* Liveness.

\* In English: "Once the node stops changing and the crashes are over, the
\* engine eventually catches up for good: running, at the node's tip, log
\* flushed, checkpoint current."
\* Literally: "CatchesUp is defined as: eventually always, up is true, srv
\*   equals chain, st.pts equals chain, pend is empty, and ck equals the
\*   record whose seq is the length of slog and whose st is st."
CatchesUp ==
    <>[](/\ up /\ srv = chain /\ st.pts = chain /\ pend = <<>>
         /\ ck = [seq |-> Len(slog), st |-> st])

-----------------------------------------------------------------------------
\* Reachability probes: each claims a situation never happens; the Probe
\* configs expect a counterexample, which shows the situation is reachable
\* and so the passing configs do exercise it.

\* In English: "Claim: the engine is never down with a stored cursor the
\* node has rolled back past."
\* Literally: "ProbeNoOfflineRollback is defined as: it is not the case
\*   that up is not true and, letting r be Recover, CommonPrefixLen of r.pts
\*   and chain is less than the length of r.pts."
ProbeNoOfflineRollback ==
    ~(~up /\ LET r == Recover IN CommonPrefixLen(r.pts, chain) < Len(r.pts))

\* In English: "Claim: a restart never has to replay a log tail (the
\* checkpoint is never behind the log while the engine is down)."
\* Literally: "ProbeNoTailReplay is defined as: it is not the case that up
\*   is not true and ck.seq is less than the length of slog."
ProbeNoTailReplay == ~(~up /\ ck.seq < Len(slog))

\* In English: "Claim: a restart never has to unwind durable actor effects
\* (design B: the recovered blocks the node dropped never name an actor's
\* counted effect)."
\* Literally: "ProbeNoDurableUnwind is defined as: it is not the case that
\*   up is not true and, letting r be Recover and ip be CommonPrefixLen of
\*   r.pts and chain, ip is less than r.conf."
ProbeNoDurableUnwind ==
    ~(~up /\ LET r == Recover IN CommonPrefixLen(r.pts, chain) < r.conf)
=============================================================================
