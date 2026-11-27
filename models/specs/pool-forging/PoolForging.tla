---------------------------- MODULE PoolForging ----------------------------
(***************************************************************************)
(* Who may forge for a stake pool, and with which operational certificate. *)
(*                                                                         *)
(* Three parties act on one pool:                                          *)
(*   - the ENGINE holds the cold key. It writes the ContinuumPool spec     *)
(*     (cr), writes the pool's keys Secret (secret) and issues every       *)
(*     operational certificate (opcert: an issue counter and the KES       *)
(*     period it starts at);                                               *)
(*   - the OPERATOR (one or two reconcilers during a leader handover)      *)
(*     turns the spec into pods through a per-implementation driver, from  *)
(*     a snapshot of the spec that can be stale;                           *)
(*   - KUBERNETES and the cluster: a pod is created, started by a kubelet  *)
(*     that mounts the Secret at that moment, told to terminate, killed    *)
(*     after its grace period, or stranded on a node that stops            *)
(*     answering while its process keeps forging.                          *)
(*                                                                         *)
(* The design the model checks has four rules:                             *)
(*   1. THE NAME IS THE FENCE. A pool's keyed pod has one name, the same   *)
(*      for every implementation and every image digest. The API server    *)
(*      refuses a second object with that name, so no operator check is    *)
(*      part of the exclusion.                                             *)
(*   2. A FINALIZER KEEPS THE NAME. The pod object stays until its         *)
(*      kubelet reports the containers stopped or the node is known dead.  *)
(*   3. THE SECRET IS THE ENGINE'S GATE. Before it issues, retires or      *)
(*      closes an equivocation scenario, the engine removes the Secret,    *)
(*      then reads the pod list live and waits for it to be empty. A pod   *)
(*      created late cannot start without the Secret.                      *)
(*   4. THE COUNTER COMES FROM THE CHAIN. A new opcert takes the highest   *)
(*      counter forged so far plus one, and the KES period the chain is    *)
(*      in. The Secret is stamped with the spec generation it was issued   *)
(*      for, which is what makes an interrupted issue safe to resume.      *)
(*                                                                         *)
(* Mutation replaces one rule with a simpler one; each *Bug.cfg names the  *)
(* invariant it then breaks.                                               *)
(*                                                                         *)
(* This is a design model. It constrains internal/operator (the           *)
(* controller, its drivers and its image roll), the engine's opcert        *)
(* issuer and internal/keys (Store.Pool, Store.OpCert).                    *)
(*                                                                         *)
(* HOW TO READ THE SYMBOLS: see the "Reading a formula literally" table   *)
(* in models/specs/engine-recovery/README.md.                              *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    Pools,          \* the stake pools
    Impls,          \* the node implementations a driver exists for
    Reconcilers,    \* operator processes that may act (two = a handover)
    Deposed,        \* reconcilers that never refresh (StaleLeader only)
    MaxEvo,         \* MaxKESEvolutions: periods one KES key is good for
    MaxPeriod,      \* the last KES period the clock reaches (bound)
    MaxDig,         \* how many times a pool's image digest moves (bound)
    MaxReq,         \* how many lifecycle requests the engine makes (bound)
    MaxScen,        \* how many equivocation scenarios are started (bound)
    MaxCrashes,     \* how many times the engine dies mid-issue (bound)
    MaxDeaths,      \* how many times a pod's process dies (bound)
    MaxLosses,      \* how many times a pod's node is lost (bound)
    CPraos,         \* TRUE: a counter may exceed the chain's by at most 1
    SwitchInPlace,  \* TRUE: the engine changes impl in one spec write
    Mutation        \* "none", or one deliberate mistake (see the README)

VARIABLES
    \* Written by the engine.
    cr,       \* the ContinuumPool spec: [impl, phase, gen]
    want,     \* the engine's durable plan for the pool: [impl, phase]
    secret,   \* the opcert in the keys Secret: [ino, kes, gen], or Blank
    retired,  \* the pool's retirement has taken effect
    scen,     \* the equivocation scenario: "off", "on" or "ending"
    ectr,     \* the engine's own record of the last counter it issued
    eobs,     \* EngineCachedList only: the engine's cached "a pod exists"
    seen,     \* StaleCounter only: the engine's lagging chain counter
    checked,  \* IssueWithoutBlank only: the pod list was read as empty
    \* The cluster.
    pods,     \* every pod object, and every process that outlived its object
    dig,      \* the image digest each pool currently resolves to
    \* The chain and the clock.
    chainCtr, \* the highest counter the pool has forged with (-1: none)
    onKes,    \* the KES start period of the opcert forged at chainCtr
    fault,    \* the first rule a forged block broke, or "none"
    period,   \* the current KES period
    \* The operator.
    obs,      \* each reconciler's snapshot: [impl, phase, scen, has]
    \* Bounds.
    used      \* how much of each bound is spent

\* In English: "The variables grouped by who owns them."
\* Literally: "engineVars is defined as the tuple of cr, want, secret,
\*   retired, scen, ectr, eobs, seen and checked."
engineVars == <<cr, want, secret, retired, scen, ectr, eobs, seen, checked>>
\* Literally: "kubeVars is defined as the tuple of pods and dig."
kubeVars   == <<pods, dig>>
\* Literally: "chainVars is defined as the tuple of chainCtr, onKes, fault
\*   and period."
chainVars  == <<chainCtr, onKes, fault, period>>
\* Literally: "vars is defined as the tuple of engineVars, kubeVars,
\*   chainVars, obs and used."
vars       == <<engineVars, kubeVars, chainVars, obs, used>>

-----------------------------------------------------------------------------
\* Helpers.

\* In English: "The larger of a and b."
\* Literally: "Max of a and b is defined as: if a is greater than b then a,
\*   else b."
Max(a, b) == IF a > b THEN a ELSE b

\* In English: "The opcert of a pod whose process is not running."
\* Literally: "NoCert is defined as the record whose ino is -1 and whose kes
\*   is -1."
NoCert == [ino |-> -1, kes |-> -1]

\* In English: "A keys Secret with no opcert in it: nothing can start from
\* it."
\* Literally: "Blank is defined as the record whose ino, kes and gen are
\*   all -1."
Blank == [ino |-> -1, kes |-> -1, gen |-> -1]

\* In English: "The opcert a kubelet mounts from secret s."
\* Literally: "Mounted of s is defined as the record whose ino is s.ino and
\*   whose kes is s.kes."
Mounted(s) == [ino |-> s.ino, kes |-> s.kes]

\* In English: "The ledger's counter rule: an opcert counter n is accepted
\* after counter m if it is not lower and, under Praos, at most one higher."
\* Literally: "SpecCounterOk of m and n is defined as: m is at most n, and
\*   CPraos implies n is at most m plus 1."
SpecCounterOk(m, n) == m <= n /\ (CPraos => n <= m + 1)

\* In English: "The counter the ledger compares against for pool p: 0 until
\* the pool has forged."
\* Literally: "LedgerCtr of p is defined as Max of chainCtr of p and 0."
LedgerCtr(p) == Max(chainCtr[p], 0)

\* In English: "A KES key that starts at period k can sign in the current
\* period."
\* Literally: "KesOk of k is defined as: k is at most period, and period is
\*   less than k plus MaxEvo."
KesOk(k) == k <= period /\ period < k + MaxEvo

\* In English: "The pod has a running process that holds the pool's keys. A
\* terminating pod still counts until it is killed; so does a process whose
\* pod object is gone."
\* Literally: "Alive of q is defined as: q.cert does not equal NoCert, and
\*   q.st is a member of the set of "running", "terminating" and "orphan"."
Alive(q) == q.cert /= NoCert /\ q.st \in {"running", "terminating", "orphan"}

\* In English: "The pod objects of pool p in the API server. An orphan is a
\* process, not an object."
\* Literally: "Objects of p is defined as the set of q in pods such that
\*   q.pool equals p and q.st does not equal "orphan"."
Objects(p) == {q \in pods : q.pool = p /\ q.st /= "orphan"}

\* In English: "The running key-holding processes of pool p, wherever they
\* are and whichever implementation they run."
\* Literally: "Forgers of p is defined as the set of q in pods such that
\*   q.pool equals p and Alive of q."
Forgers(p) == {q \in pods : q.pool = p /\ Alive(q)}

\* In English: "The name the API server knows a pod by. In the design it
\* depends on the pool and on whether the pod is the scenario's duplicate,
\* and on nothing else. FencePerDriver and CacheFence put the
\* implementation in the name (one StatefulSet per driver); RollSurge puts
\* the image digest in it (a new StatefulSet per image)."
\* Literally: "Name of p, role, i and d is defined as the tuple of p, role,
\*   i if Mutation is a member of the set of "FencePerDriver" and
\*   "CacheFence" else "any", and d if Mutation equals "RollSurge" else 0."
Name(p, role, i, d) ==
    <<p, role,
      IF Mutation \in {"FencePerDriver", "CacheFence"} THEN i ELSE "any",
      IF Mutation = "RollSurge" THEN d ELSE 0>>

\* In English: "No pod object has name n. The API server answers this, at
\* the moment of the create; it is not something the operator looks up."
\* Literally: "NameFree of n is defined as: there does not exist a q in pods
\*   such that q.st does not equal "orphan" and Name of q.pool, q.role,
\*   q.impl and q.dig equals n."
NameFree(n) ==
    ~\E q \in pods : q.st /= "orphan" /\ Name(q.pool, q.role, q.impl, q.dig) = n

\* In English: "What the engine believes about whether pool p has any pod
\* object. The design reads the API server; EngineCachedList reads an
\* informer cache."
\* Literally: "EngineSeesNoPods of p is defined as: if Mutation equals
\*   "EngineCachedList" then eobs of p is not true, else Objects of p is
\*   empty."
EngineSeesNoPods(p) ==
    IF Mutation = "EngineCachedList" THEN ~eobs[p] ELSE Objects(p) = {}

\* In English: "How many periods before expiry the engine starts a
\* rotation. RotateAtExpiry waits until the key has already expired."
\* Literally: "Margin is defined as: if Mutation equals "RotateAtExpiry"
\*   then 0, else 1."
Margin == IF Mutation = "RotateAtExpiry" THEN 0 ELSE 1

\* In English: "The opcert in pool p's Secret is in, or past, the last
\* period the engine lets it reach."
\* Literally: "RotationDue of p is defined as: secret of p does not equal
\*   Blank, and period is at least secret of p's kes plus MaxEvo minus
\*   Margin."
RotationDue(p) ==
    secret[p] /= Blank /\ period >= secret[p].kes + MaxEvo - Margin

\* In English: "The counter the engine gives a new opcert. The design reads
\* the chain: one above the highest counter forged, or 0 for a pool that
\* has never forged. BookCounter adds one to the engine's own record;
\* StaleCounter reads a view of the chain that lags."
\* Literally: "NextCounter of p is defined as: if Mutation equals
\*   "BookCounter" then ectr of p plus 1, else if Mutation equals
\*   "StaleCounter" then seen of p plus 1, else chainCtr of p plus 1."
NextCounter(p) ==
    IF Mutation = "BookCounter" THEN ectr[p] + 1
    ELSE IF Mutation = "StaleCounter" THEN seen[p] + 1
    ELSE chainCtr[p] + 1

\* In English: "The KES period the engine writes into a new opcert. The
\* design uses the chain's current period. KesFromGenesis always writes 0;
\* KesFromWallClock uses a clock that runs one period ahead."
\* Literally: "NextKes is defined as: if Mutation equals "KesFromGenesis"
\*   then 0, else if Mutation equals "KesFromWallClock" then period plus 1,
\*   else period."
NextKes ==
    IF Mutation = "KesFromGenesis" THEN 0
    ELSE IF Mutation = "KesFromWallClock" THEN period + 1
    ELSE period

\* In English: "What a reconciler sees of pool p when it refreshes: the
\* spec, and whether the scenario's duplicate is asked for. Only CacheFence
\* also remembers whether a keyed pod existed; the design never consults
\* that."
\* Literally: "Snapshot of p is defined as the record whose impl is cr of
\*   p's impl, whose phase is cr of p's phase, whose scen is whether scen of
\*   p equals "on", and whose has is whether Mutation equals "CacheFence"
\*   and there exists a q in Objects of p such that q.role equals "main"."
Snapshot(p) ==
    [impl  |-> cr[p].impl,
     phase |-> cr[p].phase,
     scen  |-> scen[p] = "on",
     has   |-> Mutation = "CacheFence" /\ \E q \in Objects(p) : q.role = "main"]

\* In English: "One implementation, to start every pool on."
\* Literally: "FirstImpl is defined as some i, chosen from Impls."
FirstImpl == CHOOSE i \in Impls : TRUE

\* In English: "Spend one unit of bound f."
\* Literally: "Spend of f is defined as: used in the next state is used with
\*   f set to used.f plus 1."
Spend(f) == used' = [used EXCEPT ![f] = @ + 1]

\* In English: "Pod q becomes q2."
\* Literally: "Swap of q and q2 is defined as: pods in the next state is
\*   pods without q, together with q2."
Swap(q, q2) == pods' = (pods \ {q}) \cup {q2}

-----------------------------------------------------------------------------
\* Start.

\* In English: "Every pool exists but is dark: no opcert has been issued,
\* nothing has been forged, there are no pods, and every reconciler's
\* snapshot is current."
\* Literally: "Init is defined as: cr maps every pool to the record with
\*   impl FirstImpl, phase "dark" and gen 0; want maps every pool to the
\*   record with impl FirstImpl and phase "dark"; secret maps every pool to
\*   Blank; retired, eobs and checked map every pool to false; scen maps
\*   every pool to "off"; ectr, seen and chainCtr map every pool to -1;
\*   onKes maps every pool to -1; fault maps every pool to "none"; pods is
\*   empty; dig maps every pool to 0; period is 0; obs maps every
\*   reconciler and pool to Snapshot of that pool; and used is 0 in every
\*   field."
Init ==
    /\ cr = [p \in Pools |-> [impl |-> FirstImpl, phase |-> "dark", gen |-> 0]]
    /\ want = [p \in Pools |-> [impl |-> FirstImpl, phase |-> "dark"]]
    /\ secret = [p \in Pools |-> Blank]
    /\ retired = [p \in Pools |-> FALSE]
    /\ scen = [p \in Pools |-> "off"]
    /\ ectr = [p \in Pools |-> -1]
    /\ eobs = [p \in Pools |-> FALSE]
    /\ seen = [p \in Pools |-> -1]
    /\ checked = [p \in Pools |-> FALSE]
    /\ pods = {}
    /\ dig = [p \in Pools |-> 0]
    /\ chainCtr = [p \in Pools |-> -1]
    /\ onKes = [p \in Pools |-> -1]
    /\ fault = [p \in Pools |-> "none"]
    /\ period = 0
    /\ obs = [r \in Reconcilers |-> [p \in Pools |->
                [impl |-> FirstImpl, phase |-> "dark", scen |-> FALSE,
                 has |-> FALSE]]]
    /\ used = [req |-> 0, scen |-> 0, crash |-> 0, death |-> 0, loss |-> 0]

-----------------------------------------------------------------------------
\* The engine. Every step is decided from durable state alone (its plan,
\* the spec, the Secret, the chain and a live pod list), so an engine
\* restart between two steps changes nothing. The one step with a second
\* write, Issue, has a crashing variant.

\* In English: "At an epoch boundary the engine plans a change for pool p:
\* go live on some implementation, go dark, or retire. Retirement is final."
\* Literally: "Request of p and w is defined as: retired of p is not true,
\*   want of p's phase does not equal "retired", used.req is less than
\*   MaxReq, w does not equal want of p, w.phase equals "live" or w.impl
\*   equals want of p's impl, want in the next state is want with p set to
\*   w, Spend of "req", and every other variable is unchanged."
Request(p, w) ==
    /\ ~retired[p]
    /\ want[p].phase /= "retired"
    /\ used.req < MaxReq
    /\ w /= want[p]
    /\ (w.phase = "live" \/ w.impl = want[p].impl)
    /\ want' = [want EXCEPT ![p] = w]
    /\ Spend("req")
    /\ UNCHANGED <<cr, secret, retired, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs>>

\* In English: "The engine takes a live pool dark: because the plan says
\* dark or retired, because the plan names another implementation, or
\* because the opcert is due for rotation. It never does so during a
\* scenario. With SwitchInPlace an implementation change alone does not
\* take the pool dark."
\* Literally: "Hold of p is defined as: retired of p is not true, cr of p's
\*   phase equals "live", scen of p equals "off", and want of p's phase
\*   does not equal "live", or RotationDue of p, or SwitchInPlace is not
\*   true and want of p's impl does not equal cr of p's impl; cr in the next
\*   state is cr with p's phase set to "dark" and p's gen increased by 1;
\*   and every other variable is unchanged."
Hold(p) ==
    /\ ~retired[p]
    /\ cr[p].phase = "live"
    /\ scen[p] = "off"
    /\ \/ want[p].phase /= "live"
       \/ RotationDue(p)
       \/ ~SwitchInPlace /\ want[p].impl /= cr[p].impl
    /\ cr' = [cr EXCEPT ![p].phase = "dark", ![p].gen = @ + 1]
    /\ UNCHANGED <<want, secret, retired, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "SwitchInPlace only: the engine names the new implementation
\* in a live spec with one write and leaves the opcert alone. The operator
\* then has to stop the old pod and start the new one by itself."
\* Literally: "SwitchDirect of p is defined as: SwitchInPlace is true,
\*   retired of p is not true, cr of p's phase equals "live", scen of p
\*   equals "off", want of p's phase equals "live", want of p's impl does
\*   not equal cr of p's impl, RotationDue of p is not true, cr in the next
\*   state is cr with p's impl set to want of p's impl and p's gen increased
\*   by 1, and every other variable is unchanged."
SwitchDirect(p) ==
    /\ SwitchInPlace
    /\ ~retired[p]
    /\ cr[p].phase = "live"
    /\ scen[p] = "off"
    /\ want[p].phase = "live"
    /\ want[p].impl /= cr[p].impl
    /\ ~RotationDue(p)
    /\ cr' = [cr EXCEPT ![p].impl = want[p].impl, ![p].gen = @ + 1]
    /\ UNCHANGED <<want, secret, retired, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "The pool is dark and the engine removes the opcert from the
\* Secret: always before retiring, and before going live unless the Secret
\* already holds an opcert issued for this very dark spell that is not due
\* for rotation. From here no kubelet can start a keyed process for the
\* pool. IssueWithoutBlank skips this step before going live."
\* Literally: "BlankSecret of p is defined as: retired of p is not true, cr
\*   of p's phase equals "dark", secret of p does not equal Blank, and want
\*   of p's phase equals "retired", or want of p's phase equals "live" and
\*   Mutation does not equal "IssueWithoutBlank" and secret of p's gen does
\*   not equal cr of p's gen or RotationDue of p; secret in the next state
\*   is secret with p set to Blank; and every other variable is unchanged."
BlankSecret(p) ==
    /\ ~retired[p]
    /\ cr[p].phase = "dark"
    /\ secret[p] /= Blank
    /\ \/ want[p].phase = "retired"
       \/ /\ want[p].phase = "live"
          /\ Mutation /= "IssueWithoutBlank"
          /\ (secret[p].gen /= cr[p].gen \/ RotationDue(p))
    /\ secret' = [secret EXCEPT ![p] = Blank]
    /\ UNCHANGED <<cr, want, retired, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "IssueWithoutBlank only: the engine reads the pod list,
\* finds it empty and remembers that. The Secret still holds the old
\* opcert, so a pod created after this read can start with it. It also
\* starts the re-issue of an opcert that came due before going live."
\* Literally: "CheckPods of p is defined as: Mutation equals
\*   "IssueWithoutBlank", retired of p is not true, want of p's phase equals
\*   "live", cr of p's phase equals "dark", secret of p's gen does not equal
\*   cr of p's gen or RotationDue of p, checked of p is not true, Objects
\*   of p is empty, checked in the next state is checked with p set to
\*   true, and every other variable is unchanged."
CheckPods(p) ==
    /\ Mutation = "IssueWithoutBlank"
    /\ ~retired[p]
    /\ want[p].phase = "live"
    /\ cr[p].phase = "dark"
    /\ (secret[p].gen /= cr[p].gen \/ RotationDue(p))
    /\ ~checked[p]
    /\ Objects(p) = {}
    /\ checked' = [checked EXCEPT ![p] = TRUE]
    /\ UNCHANGED <<cr, want, secret, retired, scen, ectr, eobs, seen,
                   kubeVars, chainVars, obs, used>>

\* In English: "The pool is dark, the Secret is blank and the pod list read
\* from the API server is empty: no process holds an older opcert and none
\* can start. The engine issues an opcert (counter from the chain, KES
\* period from the chain) and writes it to the Secret, stamped with the
\* spec generation of this dark spell. If recorded is false the engine dies
\* right after the Secret write, before its own record of the counter."
\* Literally: "IssueWith of p and recorded is defined as: retired of p is
\*   not true, want of p's phase equals "live", cr of p's phase equals
\*   "dark", and if Mutation equals "IssueWithoutBlank" then checked of p
\*   is true and secret of p's gen does not equal cr of p's gen or
\*   RotationDue of p, else secret of p equals Blank and EngineSeesNoPods
\*   of p; secret in the next state
\*   is secret with p set to the record whose ino is NextCounter of p, whose
\*   kes is NextKes and whose gen is cr of p's gen; checked in the next
\*   state is checked with p set to false; and if recorded then ectr in the
\*   next state is ectr with p set to NextCounter of p and used is
\*   unchanged, else used.crash is less than MaxCrashes and Spend of "crash"
\*   and ectr is unchanged; and every other variable is unchanged."
IssueWith(p, recorded) ==
    /\ ~retired[p]
    /\ want[p].phase = "live"
    /\ cr[p].phase = "dark"
    /\ IF Mutation = "IssueWithoutBlank"
       THEN checked[p] /\ (secret[p].gen /= cr[p].gen \/ RotationDue(p))
       ELSE secret[p] = Blank /\ EngineSeesNoPods(p)
    /\ secret' = [secret EXCEPT ![p] =
                    [ino |-> NextCounter(p), kes |-> NextKes, gen |-> cr[p].gen]]
    /\ checked' = [checked EXCEPT ![p] = FALSE]
    /\ IF recorded
       THEN ectr' = [ectr EXCEPT ![p] = NextCounter(p)] /\ UNCHANGED used
       ELSE used.crash < MaxCrashes /\ Spend("crash") /\ UNCHANGED ectr
    /\ UNCHANGED <<cr, want, retired, scen, eobs, seen,
                   kubeVars, chainVars, obs>>

\* In English: "The engine issues and records it."
\* Literally: "Issue of p is defined as IssueWith of p and true."
Issue(p) == IssueWith(p, TRUE)

\* In English: "The engine issues and dies before recording it."
\* Literally: "IssueCrash of p is defined as IssueWith of p and false."
IssueCrash(p) == IssueWith(p, FALSE)

\* In English: "The Secret holds an opcert issued for this dark spell that
\* is not due for rotation, so the engine writes the pool live on the
\* planned implementation."
\* Literally: "GoLive of p is defined as: retired of p is not true, want of
\*   p's phase equals "live", cr of p's phase equals "dark", secret of p
\*   does not equal Blank, secret of p's gen equals cr of p's gen,
\*   RotationDue of p is not true, cr in the next state is cr with p set to
\*   the record whose impl is want of p's impl, whose phase is "live" and
\*   whose gen is cr of p's gen plus 1, and every other variable is
\*   unchanged."
GoLive(p) ==
    /\ ~retired[p]
    /\ want[p].phase = "live"
    /\ cr[p].phase = "dark"
    /\ secret[p] /= Blank
    /\ secret[p].gen = cr[p].gen
    /\ ~RotationDue(p)
    /\ cr' = [cr EXCEPT ![p] = [impl |-> want[p].impl, phase |-> "live",
                                gen |-> cr[p].gen + 1]]
    /\ UNCHANGED <<want, secret, retired, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "The pool is dark, its Secret is blank and the live pod list
\* is empty. Only now does the engine let the retirement take effect (it
\* submits the certificate; the epoch boundary it names follows).
\* RetireBeforeGone does it as soon as the spec says dark."
\* Literally: "Retire of p is defined as: retired of p is not true, want of
\*   p's phase equals "retired", cr of p's phase equals "dark", Mutation
\*   equals "RetireBeforeGone" or secret of p equals Blank and
\*   EngineSeesNoPods of p, retired in the next state is retired with p set
\*   to true, and every other variable is unchanged."
Retire(p) ==
    /\ ~retired[p]
    /\ want[p].phase = "retired"
    /\ cr[p].phase = "dark"
    /\ \/ Mutation = "RetireBeforeGone"
       \/ secret[p] = Blank /\ EngineSeesNoPods(p)
    /\ retired' = [retired EXCEPT ![p] = TRUE]
    /\ UNCHANGED <<cr, want, secret, scen, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "The engine opens an equivocation scenario on a pool that is
\* live and settled: it creates the duplicate ContinuumPool
\* (UnsafeDuplicateOf) with a Secret of its own that copies the pool's
\* keys, and annotates the network."
\* Literally: "ScenarioStart of p is defined as: scen of p equals "off",
\*   used.scen is less than MaxScen, retired of p is not true, want of p's
\*   phase equals "live", cr of p's phase equals "live", want of p's impl
\*   equals cr of p's impl, RotationDue of p is not true, scen in the next
\*   state is scen with p set to "on", Spend of "scen", and every other
\*   variable is unchanged."
ScenarioStart(p) ==
    /\ scen[p] = "off"
    /\ used.scen < MaxScen
    /\ ~retired[p]
    /\ want[p].phase = "live"
    /\ cr[p].phase = "live"
    /\ want[p].impl = cr[p].impl
    /\ ~RotationDue(p)
    /\ scen' = [scen EXCEPT ![p] = "on"]
    /\ Spend("scen")
    /\ UNCHANGED <<cr, want, secret, retired, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs>>

\* In English: "The engine ends the scenario: it deletes the duplicate's
\* Secret and ContinuumPool and removes the annotation. The duplicate's
\* process may still be running."
\* Literally: "ScenarioEnd of p is defined as: scen of p equals "on", scen
\*   in the next state is scen with p set to "ending", and every other
\*   variable is unchanged."
ScenarioEnd(p) ==
    /\ scen[p] = "on"
    /\ scen' = [scen EXCEPT ![p] = "ending"]
    /\ UNCHANGED <<cr, want, secret, retired, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "The engine reads the pod list live, finds no duplicate pod
\* object and declares the scenario over. DuplicateOutlives declares it
\* over once the duplicate ContinuumPool is deleted, without looking at
\* pods."
\* Literally: "ScenarioClose of p is defined as: scen of p equals "ending",
\*   Mutation equals "DuplicateOutlives" or there does not exist a q in
\*   Objects of p such that q.role equals "dup", scen in the next state is
\*   scen with p set to "off", and every other variable is unchanged."
ScenarioClose(p) ==
    /\ scen[p] = "ending"
    /\ \/ Mutation = "DuplicateOutlives"
       \/ ~\E q \in Objects(p) : q.role = "dup"
    /\ scen' = [scen EXCEPT ![p] = "off"]
    /\ UNCHANGED <<cr, want, secret, retired, ectr, eobs, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "EngineCachedList only: the engine's informer catches up
\* with the pod list."
\* Literally: "EngineCacheCatchUp of p is defined as: Mutation equals
\*   "EngineCachedList", eobs of p does not equal whether Objects of p is
\*   non-empty, eobs in the next state is eobs with p set to whether Objects
\*   of p is non-empty, and every other variable is unchanged."
EngineCacheCatchUp(p) ==
    /\ Mutation = "EngineCachedList"
    /\ eobs[p] /= (Objects(p) /= {})
    /\ eobs' = [eobs EXCEPT ![p] = (Objects(p) /= {})]
    /\ UNCHANGED <<cr, want, secret, retired, scen, ectr, seen, checked,
                   kubeVars, chainVars, obs, used>>

\* In English: "StaleCounter only: the engine's lagging view of the chain
\* catches up with the pool's counter."
\* Literally: "SeenCatchUp of p is defined as: Mutation equals
\*   "StaleCounter", seen of p does not equal chainCtr of p, seen in the
\*   next state is seen with p set to chainCtr of p, and every other
\*   variable is unchanged."
SeenCatchUp(p) ==
    /\ Mutation = "StaleCounter"
    /\ seen[p] /= chainCtr[p]
    /\ seen' = [seen EXCEPT ![p] = chainCtr[p]]
    /\ UNCHANGED <<cr, want, secret, retired, scen, ectr, eobs, checked,
                   kubeVars, chainVars, obs, used>>

-----------------------------------------------------------------------------
\* The operator. A reconciler decides from its snapshot; the API server
\* decides whether a create succeeds.

\* In English: "Reconciler r refreshes its snapshot of pool p (an informer
\* event, or a live read). A deposed leader under StaleLeader never does."
\* Literally: "Observe of r and p is defined as: Mutation does not equal
\*   "StaleLeader" or r is not a member of Deposed, obs of r of p does not
\*   equal Snapshot of p, obs in the next state is obs with r's p set to
\*   Snapshot of p, and every other variable is unchanged."
Observe(r, p) ==
    /\ (Mutation /= "StaleLeader" \/ r \notin Deposed)
    /\ obs[r][p] /= Snapshot(p)
    /\ obs' = [obs EXCEPT ![r][p] = Snapshot(p)]
    /\ UNCHANGED <<engineVars, kubeVars, chainVars, used>>

\* In English: "Reconciler r believes pool p is live and creates its keyed
\* pod, on the implementation its snapshot names and the current image.
\* The API server accepts the create only if the name is free. CacheFence
\* also requires that the snapshot showed no keyed pod."
\* Literally: "CreateMain of r and p is defined as: obs of r of p's phase
\*   equals "live", NameFree of Name of p, "main", obs of r of p's impl and
\*   dig of p, Mutation equals "CacheFence" implies obs of r of p's has is
\*   not true, pods in the next state is pods together with the record
\*   whose pool is p, role is "main", impl is obs of r of p's impl, dig is
\*   dig of p, st is "pending", cert is NoCert and lost is false, and every
\*   other variable is unchanged."
CreateMain(r, p) ==
    /\ obs[r][p].phase = "live"
    /\ NameFree(Name(p, "main", obs[r][p].impl, dig[p]))
    /\ (Mutation = "CacheFence" => ~obs[r][p].has)
    /\ pods' = pods \cup {[pool |-> p, role |-> "main", impl |-> obs[r][p].impl,
                           dig |-> dig[p], st |-> "pending", cert |-> NoCert,
                           lost |-> FALSE]}
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "Reconciler r deletes a keyed pod that its snapshot says
\* should not run: the pool is dark, the pod is the wrong implementation,
\* or the pod runs an old image. The pod starts terminating; its process
\* runs on until it is killed. RollSurge deletes an old-image pod only once
\* a new-image pod is running. DarkKeepsPod never deletes a pod for the
\* pool going dark."
\* Literally: "DeleteMain of r and q is defined as: q.role equals "main",
\*   q.st is a member of the set of "pending" and "running", and obs of r
\*   of q.pool's phase equals "dark" and Mutation does not equal
\*   "DarkKeepsPod", or q.impl does not equal obs of r of
\*   q.pool's impl, or q.dig does not equal dig of q.pool and, if Mutation
\*   equals "RollSurge", there exists an n in Objects of q.pool such that
\*   n.role equals "main", n.dig equals dig of q.pool and n.st equals
\*   "running"; Swap of q and q with st set to "terminating"; and every
\*   other variable is unchanged."
DeleteMain(r, q) ==
    /\ q.role = "main"
    /\ q.st \in {"pending", "running"}
    /\ \/ obs[r][q.pool].phase = "dark" /\ Mutation /= "DarkKeepsPod"
       \/ q.impl /= obs[r][q.pool].impl
       \/ /\ q.dig /= dig[q.pool]
          /\ Mutation = "RollSurge" =>
                \E n \in Objects(q.pool) :
                    n.role = "main" /\ n.dig = dig[q.pool] /\ n.st = "running"
    /\ Swap(q, [q EXCEPT !.st = "terminating"])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "Reconciler r believes pool p's scenario is on and creates
\* the duplicate keyed pod, on any implementation. Its name differs from
\* the pool's own pod on purpose."
\* Literally: "CreateDup of r, p and i is defined as: obs of r of p's scen
\*   is true, NameFree of Name of p, "dup", i and dig of p, pods in the next
\*   state is pods together with the record whose pool is p, role is "dup",
\*   impl is i, dig is dig of p, st is "pending", cert is NoCert and lost is
\*   false, and every other variable is unchanged."
CreateDup(r, p, i) ==
    /\ obs[r][p].scen
    /\ NameFree(Name(p, "dup", i, dig[p]))
    /\ pods' = pods \cup {[pool |-> p, role |-> "dup", impl |-> i,
                           dig |-> dig[p], st |-> "pending", cert |-> NoCert,
                           lost |-> FALSE]}
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "Reconciler r believes the scenario is not on and deletes
\* the duplicate pod."
\* Literally: "DeleteDup of r and q is defined as: q.role equals "dup", q.st
\*   is a member of the set of "pending" and "running", obs of r of q.pool's
\*   scen is not true, Swap of q and q with st set to "terminating", and
\*   every other variable is unchanged."
DeleteDup(r, q) ==
    /\ q.role = "dup"
    /\ q.st \in {"pending", "running"}
    /\ ~obs[r][q.pool].scen
    /\ Swap(q, [q EXCEPT !.st = "terminating"])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "The pod's kubelet has reported its containers stopped, or
\* its node is known dead. The operator removes its finalizer, the object
\* goes away and the name is free again."
\* Literally: "Release of q is defined as: q.st equals "stopped", pods in
\*   the next state is pods without q, and every other variable is
\*   unchanged."
Release(q) ==
    /\ q.st = "stopped"
    /\ pods' = pods \ {q}
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "LostIsGone only: a terminating pod's node has not answered
\* for a while, so the operator treats the pod as gone and removes its
\* finalizer. A process that was running keeps running, with no object."
\* Literally: "LostRelease of q is defined as: Mutation equals "LostIsGone",
\*   q.st equals "terminating", q.lost is true, and if Alive of q then Swap
\*   of q and q with st set to "orphan" else pods in the next state is pods
\*   without q, and every other variable is unchanged."
LostRelease(q) ==
    /\ Mutation = "LostIsGone"
    /\ q.st = "terminating"
    /\ q.lost
    /\ IF Alive(q) THEN Swap(q, [q EXCEPT !.st = "orphan"]) ELSE pods' = pods \ {q}
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

-----------------------------------------------------------------------------
\* Kubernetes and the cluster.

\* In English: "The key material a kubelet would mount for pod q right now,
\* or Blank if the pod cannot start. The pool's own pod mounts the pool's
\* Secret. The duplicate mounts its own Secret, which exists only while the
\* scenario is on and copies the pool's opcert; DuplicateSharedSecret makes
\* it mount the pool's Secret instead."
\* Literally: "Gate of q is defined as: if q.role equals "dup" and Mutation
\*   does not equal "DuplicateSharedSecret" and scen of q.pool does not
\*   equal "on" then Blank, else secret of q.pool."
Gate(q) ==
    IF q.role = "dup" /\ Mutation /= "DuplicateSharedSecret" /\ scen[q.pool] /= "on"
    THEN Blank
    ELSE secret[q.pool]

\* In English: "The kubelet starts a pending pod's process. It mounts
\* whatever opcert the Secret holds at this moment, not what it held when
\* the pod was created. With nothing to mount the pod stays pending."
\* Literally: "Start of q is defined as: q.st equals "pending", q.lost is
\*   not true, Gate of q does not equal Blank, Swap of q and q with st set
\*   to "running" and cert set to Mounted of Gate of q, and every other
\*   variable is unchanged."
Start(q) ==
    /\ q.st = "pending"
    /\ ~q.lost
    /\ Gate(q) /= Blank
    /\ Swap(q, [q EXCEPT !.st = "running", !.cert = Mounted(Gate(q))])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "A terminating pod's grace period ends, or its process
\* exits, and the kubelet reports the containers stopped. Until this step a
\* terminating pod can forge. A kubelet that cannot be reached cannot
\* report."
\* Literally: "Kill of q is defined as: q.st equals "terminating", q.lost is
\*   not true, Swap of q and q with st set to "stopped" and cert set to
\*   NoCert, and every other variable is unchanged."
Kill(q) ==
    /\ q.st = "terminating"
    /\ ~q.lost
    /\ Swap(q, [q EXCEPT !.st = "stopped", !.cert = NoCert])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "A running pod's process dies. The pod object stays and the
\* kubelet will start the container again, mounting the Secret as it is
\* then."
\* Literally: "PodDies of q is defined as: q.st equals "running", q.lost is
\*   not true, used.death is less than MaxDeaths, Swap of q and q with st
\*   set to "pending" and cert set to NoCert, Spend of "death", and every
\*   other variable except pods and used is unchanged."
PodDies(q) ==
    /\ q.st = "running"
    /\ ~q.lost
    /\ used.death < MaxDeaths
    /\ Swap(q, [q EXCEPT !.st = "pending", !.cert = NoCert])
    /\ Spend("death")
    /\ UNCHANGED <<engineVars, dig, chainVars, obs>>

\* In English: "The pod's node stops answering. Its process, if any, keeps
\* running and forging; nothing the API server says reaches the kubelet."
\* Literally: "NodeLost of q is defined as: q.st is a member of the set of
\*   "pending", "running" and "terminating", q.lost is not true, used.loss
\*   is less than MaxLosses, Swap of q and q with lost set to true, Spend of
\*   "loss", and every other variable except pods and used is unchanged."
NodeLost(q) ==
    /\ q.st \in {"pending", "running", "terminating"}
    /\ ~q.lost
    /\ used.loss < MaxLosses
    /\ Swap(q, [q EXCEPT !.lost = TRUE])
    /\ Spend("loss")
    /\ UNCHANGED <<engineVars, dig, chainVars, obs>>

\* In English: "The node answers again. A process whose pod object is gone
\* is killed by its kubelet; any other pod carries on from where it was."
\* Literally: "NodeHeal of q is defined as: q.lost is true, and if q.st
\*   equals "orphan" then pods in the next state is pods without q, else
\*   Swap of q and q with lost set to false, and every other variable is
\*   unchanged."
NodeHeal(q) ==
    /\ q.lost
    /\ IF q.st = "orphan" THEN pods' = pods \ {q}
       ELSE Swap(q, [q EXCEPT !.lost = FALSE])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "The node is known dead (its Node object is deleted, or the
\* machine is confirmed off). Its processes are gone."
\* Literally: "NodeDead of q is defined as: q.lost is true, and if q.st
\*   equals "orphan" then pods in the next state is pods without q, else
\*   Swap of q and q with st set to "stopped", cert set to NoCert and lost
\*   set to false, and every other variable is unchanged."
NodeDead(q) ==
    /\ q.lost
    /\ IF q.st = "orphan" THEN pods' = pods \ {q}
       ELSE Swap(q, [q EXCEPT !.st = "stopped", !.cert = NoCert, !.lost = FALSE])
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "NoFinalizer only: someone force-deletes a pod on a lost
\* node (kubectl delete --force, or an eviction). With no finalizer the
\* object goes at once and the name is free, while a running process keeps
\* running. In the design a force delete is an ordinary delete, because the
\* finalizer keeps the object."
\* Literally: "ForceDelete of q is defined as: Mutation equals
\*   "NoFinalizer", q.st is a member of the set of "pending", "running" and
\*   "terminating", q.lost is true, and if Alive of q then Swap of q and q
\*   with st set to "orphan" else pods in the next state is pods without q,
\*   and every other variable is unchanged."
ForceDelete(q) ==
    /\ Mutation = "NoFinalizer"
    /\ q.st \in {"pending", "running", "terminating"}
    /\ q.lost
    /\ IF Alive(q) THEN Swap(q, [q EXCEPT !.st = "orphan"]) ELSE pods' = pods \ {q}
    /\ UNCHANGED <<engineVars, dig, chainVars, obs, used>>

\* In English: "The operator resolves pool p's image to a new digest (a
\* moved tag, or `continuum image set`). The keys Secret is not touched."
\* Literally: "ImageRoll of p is defined as: dig of p is less than MaxDig,
\*   dig in the next state is dig with p increased by 1, and every other
\*   variable is unchanged."
ImageRoll(p) ==
    /\ dig[p] < MaxDig
    /\ dig' = [dig EXCEPT ![p] = @ + 1]
    /\ UNCHANGED <<engineVars, pods, chainVars, obs, used>>

-----------------------------------------------------------------------------
\* The chain and the clock.

\* In English: "The rule a block forged now with opcert c would break for
\* pool p, checked in this order: the KES key is not valid yet; the counter
\* is below the chain's; the counter jumps past what Praos accepts; the
\* counter equals the chain's but the opcert is a different one."
\* Literally: "Breaks of p and c is defined as: if c.kes is greater than
\*   period then "kes", else if c.ino is less than chainCtr of p then
\*   "back", else if SpecCounterOk of LedgerCtr of p and c.ino is not true
\*   then "jump", else if c.ino equals chainCtr of p and c.kes does not
\*   equal onKes of p then "reuse", else "none"."
Breaks(p, c) ==
    IF c.kes > period THEN "kes"
    ELSE IF c.ino < chainCtr[p] THEN "back"
    ELSE IF ~SpecCounterOk(LedgerCtr(p), c.ino) THEN "jump"
    ELSE IF c.ino = chainCtr[p] /\ c.kes /= onKes[p] THEN "reuse"
    ELSE "none"

\* In English: "A running key-holding process wins a slot and forges. A
\* node cannot sign with a KES key past its last period, so it stays silent
\* then. A block that breaks a rule is remembered as the pool's fault; a
\* good block with a higher counter moves the chain's counter."
\* Literally: "Forge of q is defined as: Alive of q, period is less than
\*   q.cert.kes plus MaxEvo, and letting p be q.pool, c be q.cert and b be
\*   Breaks of p and c: if b does not equal "none" then fault of p equals
\*   "none" and fault in the next state is fault with p set to b and
\*   chainCtr and onKes are unchanged, else c.ino is greater than chainCtr
\*   of p and chainCtr in the next state is chainCtr with p set to c.ino
\*   and onKes in the next state is onKes with p set to c.kes and fault is
\*   unchanged; and every other variable is unchanged."
Forge(q) ==
    /\ Alive(q)
    /\ period < q.cert.kes + MaxEvo
    /\ LET p == q.pool
           c == q.cert
           b == Breaks(p, c)
       IN  IF b /= "none"
           THEN /\ fault[p] = "none"
                /\ fault' = [fault EXCEPT ![p] = b]
                /\ UNCHANGED <<chainCtr, onKes>>
           ELSE /\ c.ino > chainCtr[p]
                /\ chainCtr' = [chainCtr EXCEPT ![p] = c.ino]
                /\ onKes' = [onKes EXCEPT ![p] = c.kes]
                /\ UNCHANGED fault
    /\ UNCHANGED <<engineVars, kubeVars, period, obs, used>>

\* In English: "The chain enters the next KES period. TIMING ASSUMPTION:
\* once an opcert is due for rotation, the engine finishes what it has to do
\* with it (rotate it, or take the pool dark) before the period ends. So the
\* clock waits for every pool whose opcert is due while the plan or the
\* spec still says live. A pool that is dark and planned dark, or retired,
\* does not hold the clock."
\* Literally: "KesTick is defined as: period is less than MaxPeriod, for
\*   all p in Pools it is not the case that RotationDue of p and retired of
\*   p is not true and want of p's phase equals "live" or cr of p's phase
\*   equals "live", period in the next state is period plus 1, and every
\*   other variable is unchanged."
KesTick ==
    /\ period < MaxPeriod
    /\ \A p \in Pools :
          ~(/\ RotationDue(p)
            /\ ~retired[p]
            /\ (want[p].phase = "live" \/ cr[p].phase = "live"))
    /\ period' = period + 1
    /\ UNCHANGED <<engineVars, kubeVars, chainCtr, onKes, fault, obs, used>>

-----------------------------------------------------------------------------
\* The whole system.

\* In English: "Pool p has a pod of its own that is running on a reachable
\* node with a KES key valid now."
\* Literally: "Forging of p is defined as: there exists a q in pods such
\*   that q.pool equals p, q.role equals "main", q.st equals "running",
\*   q.lost is not true and KesOk of q.cert.kes."
Forging(p) ==
    \E q \in pods :
        q.pool = p /\ q.role = "main" /\ q.st = "running" /\ ~q.lost
        /\ KesOk(q.cert.kes)

\* In English: "Pool p has reached its plan and nothing is left to do for
\* it: no scenario is on, every reconciler that refreshes sees the current
\* spec, and the pool is retired with no key-holding process, dark with
\* none, or live and forging on the planned implementation."
\* Literally: "Settled of p is defined as: scen of p equals "off"; for all
\*   r in Reconcilers, if Mutation does not equal "StaleLeader" or r is not
\*   a member of Deposed then obs of r of p equals Snapshot of p; and in
\*   the case want of p's phase equals "retired", retired of p and Forgers
\*   of p is empty; in the case it equals "dark", cr of p's phase equals
\*   "dark" and Forgers of p is empty; in every other case cr of p's phase
\*   equals "live", cr of p's impl equals want of p's impl and Forging of
\*   p."
Settled(p) ==
    /\ scen[p] = "off"
    /\ \A r \in Reconcilers :
          (Mutation /= "StaleLeader" \/ r \notin Deposed) => obs[r][p] = Snapshot(p)
    /\ CASE want[p].phase = "retired" -> retired[p] /\ Forgers(p) = {}
         [] want[p].phase = "dark" -> cr[p].phase = "dark" /\ Forgers(p) = {}
         [] OTHER -> /\ cr[p].phase = "live" /\ cr[p].impl = want[p].impl
                     /\ Forging(p)

\* In English: "The run is over: every pool has reached its plan. Any other
\* state with no step left is a pool stuck short of its plan, which TLC's
\* deadlock check reports."
\* Literally: "Done is defined as: for all p in Pools, Settled of p, and
\*   vars is unchanged."
Done ==
    /\ \A p \in Pools : Settled(p)
    /\ UNCHANGED vars

\* In English: "The plans the engine can make for a pool."
\* Literally: "Plans is defined as the set of records whose impl is a member
\*   of Impls and whose phase is a member of the set of "live", "dark" and
\*   "retired"."
Plans == [impl : Impls, phase : {"live", "dark", "retired"}]

\* In English: "One step of the engine on pool p."
\* Literally: "EngineStep of p is defined as: Hold of p, or SwitchDirect of
\*   p, or BlankSecret of p, or CheckPods of p, or Issue of p, or GoLive of
\*   p, or Retire of p, or ScenarioEnd of p, or ScenarioClose of p."
EngineStep(p) ==
    \/ Hold(p) \/ SwitchDirect(p) \/ BlankSecret(p) \/ CheckPods(p)
    \/ Issue(p) \/ GoLive(p) \/ Retire(p)
    \/ ScenarioEnd(p) \/ ScenarioClose(p)

\* In English: "One step reconciler r can take on pool p once its snapshot
\* is what it is."
\* Literally: "OperatorStep of r and p is defined as: CreateMain of r and p,
\*   or there exists an i in Impls such that CreateDup of r, p and i, or
\*   there exists a q in Objects of p such that DeleteMain of r and q or
\*   DeleteDup of r and q."
OperatorStep(r, p) ==
    \/ CreateMain(r, p)
    \/ \E i \in Impls : CreateDup(r, p, i)
    \/ \E q \in Objects(p) : DeleteMain(r, q) \/ DeleteDup(r, q)

\* In English: "One step a kubelet or the operator's finalizer takes on a
\* pod without being asked."
\* Literally: "KubeStep of q is defined as: Start of q, or Kill of q, or
\*   Release of q."
KubeStep(q) == Start(q) \/ Kill(q) \/ Release(q)

\* In English: "A lost node is resolved one way or the other."
\* Literally: "NodeResolved of q is defined as: NodeHeal of q or NodeDead of
\*   q."
NodeResolved(q) == NodeHeal(q) \/ NodeDead(q)

\* In English: "A step is an engine step, an operator step, a cluster step,
\* a block, a clock tick, or the end of the run."
\* Literally: "Next is defined as: there exists a p in Pools such that
\*   EngineStep of p, or IssueCrash of p, or ScenarioStart of p, or
\*   ImageRoll of p, or EngineCacheCatchUp of p, or SeenCatchUp of p, or
\*   there exists a w in Plans such that Request of p and w, or there exists
\*   an r in Reconcilers such that Observe of r and p or OperatorStep of r
\*   and p; or there exists a q in pods such that KubeStep of q, or
\*   NodeResolved of q, or PodDies of q, or NodeLost of q, or ForceDelete of
\*   q, or LostRelease of q, or Forge of q; or KesTick; or Done."
Next ==
    \/ \E p \in Pools :
          \/ EngineStep(p) \/ IssueCrash(p) \/ ScenarioStart(p)
          \/ ImageRoll(p) \/ EngineCacheCatchUp(p) \/ SeenCatchUp(p)
          \/ \E w \in Plans : Request(p, w)
          \/ \E r \in Reconcilers : Observe(r, p) \/ OperatorStep(r, p)
    \/ \E q \in pods :
          \/ KubeStep(q) \/ NodeResolved(q)
          \/ PodDies(q) \/ NodeLost(q) \/ ForceDelete(q) \/ LostRelease(q)
          \/ Forge(q)
    \/ KesTick
    \/ Done

\* In English: "What must eventually happen if it stays possible: the
\* engine's own steps, every reconciler's refresh and actions, the
\* kubelet's start and kill, the finalizer's release, and the resolution of
\* a lost node. Requests, scenario starts, image rolls, pod deaths, node
\* losses, engine crashes and the clock get no fairness; the bounds make
\* them finite."
\* Literally: "Fairness is defined as: for all p in Pools, weak fairness of
\*   EngineStep of p, and for all r in Reconcilers weak fairness of Observe
\*   of r and p and of OperatorStep of r and p; and weak fairness of: there
\*   exists a q in pods such that KubeStep of q; and weak fairness of: there
\*   exists a q in pods such that NodeResolved of q."
Fairness ==
    /\ \A p \in Pools :
          /\ WF_vars(EngineStep(p))
          /\ \A r \in Reconcilers :
                /\ WF_vars(Observe(r, p))
                /\ WF_vars(OperatorStep(r, p))
    /\ WF_vars(\E q \in pods : KubeStep(q))
    /\ WF_vars(\E q \in pods : NodeResolved(q))

\* In English: "Start in Init; every step is a Next step. Safety only."
\* Literally: "Spec is defined as: Init holds at the start, and always every
\*   step is a Next step or leaves vars unchanged."
Spec == Init /\ [][Next]_vars

\* In English: "The same behaviours, with Fairness."
\* Literally: "LiveSpec is defined as: Spec and Fairness."
LiveSpec == Spec /\ Fairness

-----------------------------------------------------------------------------
\* Invariants.

\* In English: "An opcert as a pod holds it."
\* Literally: "Certs is defined as the set of records whose ino is a member
\*   of -1 through MaxReq plus MaxPeriod plus MaxCrashes plus 1 and whose
\*   kes is a member of -1 through MaxPeriod plus 1."
Certs == [ino : -1..(MaxReq + MaxPeriod + MaxCrashes + 1), kes : -1..(MaxPeriod + 1)]

\* In English: "Every variable has the shape it should."
\* Literally: "TypeOK is defined as: for all p in Pools, cr of p's impl is a
\*   member of Impls, its phase is a member of the set of "live" and "dark"
\*   and its gen is a natural number; want of p is a member of Plans;
\*   Mounted of secret of p is a member of Certs; retired, eobs and checked
\*   of p are Booleans; scen of p is a member of the set of "off", "on" and
\*   "ending"; ectr, seen and chainCtr of p are at least -1; onKes of p is
\*   a member of -1 through MaxPeriod; fault of p is a member of the set of
\*   "none", "kes", "back", "jump" and "reuse"; and dig of p is a member of
\*   0 through MaxDig; and for all q in pods, q.pool is a member of Pools,
\*   q.role is a member of the set of "main" and "dup", q.impl is a member
\*   of Impls, q.dig is a member of 0 through MaxDig, q.st is a member of
\*   the set of "pending", "running", "terminating", "stopped" and
\*   "orphan", q.cert is a member of Certs and q.lost is a Boolean; and
\*   period is a member of 0 through MaxPeriod; and for all r in Reconcilers
\*   and p in Pools, obs of r of p's impl is a member of Impls, its phase is
\*   a member of the set of "live" and "dark", and its scen and has are
\*   Booleans."
TypeOK ==
    /\ \A p \in Pools :
          /\ cr[p].impl \in Impls
          /\ cr[p].phase \in {"live", "dark"}
          /\ cr[p].gen \in Nat
          /\ want[p] \in Plans
          /\ Mounted(secret[p]) \in Certs
          /\ retired[p] \in BOOLEAN /\ eobs[p] \in BOOLEAN /\ checked[p] \in BOOLEAN
          /\ scen[p] \in {"off", "on", "ending"}
          /\ ectr[p] >= -1 /\ seen[p] >= -1 /\ chainCtr[p] >= -1
          /\ onKes[p] \in -1..MaxPeriod
          /\ fault[p] \in {"none", "kes", "back", "jump", "reuse"}
          /\ dig[p] \in 0..MaxDig
    /\ \A q \in pods :
          /\ q.pool \in Pools
          /\ q.role \in {"main", "dup"}
          /\ q.impl \in Impls
          /\ q.dig \in 0..MaxDig
          /\ q.st \in {"pending", "running", "terminating", "stopped", "orphan"}
          /\ q.cert \in Certs
          /\ q.lost \in BOOLEAN
    /\ period \in 0..MaxPeriod
    /\ \A r \in Reconcilers : \A p \in Pools :
          /\ obs[r][p].impl \in Impls
          /\ obs[r][p].phase \in {"live", "dark"}
          /\ obs[r][p].scen \in BOOLEAN /\ obs[r][p].has \in BOOLEAN

\* In English: "Apart from a scenario the engine asked for, no pool ever has
\* two running processes that hold its keys, wherever they run, whichever
\* implementation or image they are, and whether or not Kubernetes still
\* has a pod object for them."
\* Literally: "AtMostOneForger is defined as: for all p in Pools, if scen of
\*   p equals "off" then the number of members of Forgers of p is at most
\*   1."
AtMostOneForger ==
    \A p \in Pools : scen[p] = "off" => Cardinality(Forgers(p)) <= 1

\* In English: "A scenario adds exactly one forger and only inside its
\* window: the pool's own pod is still at most one, the duplicate is at
\* most one, and no duplicate process runs once the scenario is over."
\* Literally: "DuplicateScoped is defined as: for all p in Pools, the number
\*   of q in Forgers of p such that q.role equals "main" is at most 1, and
\*   the number of q in Forgers of p such that q.role equals "dup" is at
\*   most 1, and if scen of p equals "off" then there does not exist a q in
\*   Forgers of p such that q.role equals "dup"."
DuplicateScoped ==
    \A p \in Pools :
        /\ Cardinality({q \in Forgers(p) : q.role = "main"}) <= 1
        /\ Cardinality({q \in Forgers(p) : q.role = "dup"}) <= 1
        /\ scen[p] = "off" => ~\E q \in Forgers(p) : q.role = "dup"

\* In English: "No block a harness pod forged had a counter the ledger
\* rejects: none below the chain's, none more than one above it."
\* Literally: "ForgedBlocksValid is defined as: for all p in Pools, fault of
\*   p is not a member of the set of "back" and "jump"."
ForgedBlocksValid == \A p \in Pools : fault[p] \notin {"back", "jump"}

\* In English: "Counters on chain strictly increase from one opcert to the
\* next: no two different opcerts of a pool were ever forged with the same
\* counter, across a rotation, an engine restart or an implementation
\* switch."
\* Literally: "CounterNeverReused is defined as: for all p in Pools, fault
\*   of p does not equal "reuse"."
CounterNeverReused == \A p \in Pools : fault[p] /= "reuse"

\* In English: "No block was forged with a KES key whose first period is
\* after the block's slot."
\* Literally: "KesValidForSlot is defined as: for all p in Pools, fault of p
\*   does not equal "kes"."
KesValidForSlot == \A p \in Pools : fault[p] /= "kes"

\* In English: "The opcert in the Secret, and the opcert of every running
\* process, has a counter the ledger would accept right now. After a switch
\* the new workload therefore carries the chain's counter or the next one."
\* Literally: "SwitchCarriesCounter is defined as: for all p in Pools,
\*   secret of p equals Blank or SpecCounterOk of LedgerCtr of p and secret
\*   of p's ino, and for all q in Forgers of p, SpecCounterOk of LedgerCtr
\*   of p and q.cert.ino."
SwitchCarriesCounter ==
    \A p \in Pools :
        /\ (secret[p] = Blank \/ SpecCounterOk(LedgerCtr(p), secret[p].ino))
        /\ \A q \in Forgers(p) : SpecCounterOk(LedgerCtr(p), q.cert.ino)

\* In English: "A pool the engine plans live and has written live never has
\* a running pod of its own that cannot forge: the pod's KES key is valid
\* for the current period."
\* Literally: "NoDarkByHarness is defined as: for all q in pods, if want of
\*   q.pool's phase equals "live" and cr of q.pool's phase equals "live" and
\*   q.role equals "main" and q.st equals "running" then KesOk of
\*   q.cert.kes."
NoDarkByHarness ==
    \A q \in pods :
        (/\ want[q.pool].phase = "live"
         /\ cr[q.pool].phase = "live"
         /\ q.role = "main"
         /\ q.st = "running") => KesOk(q.cert.kes)

\* In English: "Once a pool's retirement has taken effect, nothing holds
\* its keys and runs."
\* Literally: "NoForgeAfterRetire is defined as: for all p in Pools, if
\*   retired of p is true then Forgers of p is empty."
NoForgeAfterRetire == \A p \in Pools : retired[p] => Forgers(p) = {}

-----------------------------------------------------------------------------
\* Liveness.

\* In English: "A pool that should forge eventually forges, and keeps
\* forging: once the requests and the faults stop, a pool planned live is
\* forging for good."
\* Literally: "LiveForges is defined as: for all p in Pools, eventually
\*   always, want of p's phase equals "live" implies Forging of p."
LiveForges == \A p \in Pools : <>[](want[p].phase = "live" => Forging(p))

\* In English: "A pool that should not forge eventually has nothing holding
\* its keys, for good; and a pool planned retired is eventually retired."
\* Literally: "DarkStops is defined as: for all p in Pools, eventually
\*   always, want of p's phase does not equal "live" implies Forgers of p is
\*   empty, and eventually always, want of p's phase equals "retired"
\*   implies retired of p."
DarkStops ==
    \A p \in Pools :
        /\ <>[](want[p].phase /= "live" => Forgers(p) = {})
        /\ <>[](want[p].phase = "retired" => retired[p])

-----------------------------------------------------------------------------
\* Reachability probes: each claims a situation never happens; the Probe
\* configs expect a counterexample, which shows the situation is reachable
\* and so the passing configs do exercise it.

\* In English: "Claim: a scenario never gets two forgers running. (This is
\* the deliberate equivocation continuum-forger and the fork monitor are
\* tested with.)"
\* Literally: "ProbeNoIntendedEquivocation is defined as: for all p in
\*   Pools, the number of members of Forgers of p is at most 1."
ProbeNoIntendedEquivocation == \A p \in Pools : Cardinality(Forgers(p)) <= 1

\* In English: "Claim: a pool never forges under a second opcert (no
\* rotation, switch or return from dark ever completes)."
\* Literally: "ProbeNoSecondCert is defined as: for all p in Pools, chainCtr
\*   of p is less than 1."
ProbeNoSecondCert == \A p \in Pools : chainCtr[p] < 1

\* In English: "Claim: the fence is never held by a process on a lost node
\* (a terminating pod that still forges while the spec says dark)."
\* Literally: "ProbeNoZombie is defined as: there does not exist a q in pods
\*   such that q.st equals "terminating" and q.lost is true and Alive of q
\*   and cr of q.pool's phase equals "dark"."
ProbeNoZombie ==
    ~\E q \in pods :
        q.st = "terminating" /\ q.lost /\ Alive(q) /\ cr[q.pool].phase = "dark"

\* In English: "Claim: no pool ever retires."
\* Literally: "ProbeNoRetire is defined as: for all p in Pools, retired of p
\*   is not true."
ProbeNoRetire == \A p \in Pools : ~retired[p]

\* In English: "Claim: a pod object never exists for a pool whose Secret is
\* blank after its retirement (a stale reconciler never creates one late)."
\* Literally: "ProbeNoLatePod is defined as: for all p in Pools, if retired
\*   of p is true then Objects of p is empty."
ProbeNoLatePod == \A p \in Pools : retired[p] => Objects(p) = {}

\* In English: "Claim: an implementation switch never completes (no pod of
\* a second implementation ever runs for a pool)."
\* Literally: "ProbeNoSwitch is defined as: there does not exist a q in pods
\*   such that q.role equals "main" and q.st equals "running" and q.impl
\*   does not equal FirstImpl."
ProbeNoSwitch ==
    ~\E q \in pods : q.role = "main" /\ q.st = "running" /\ q.impl /= FirstImpl
=============================================================================
