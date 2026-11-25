// Package operator turns ContinuumPool and ContinuumNetwork specs into node
// workloads: a StatefulSet, headless Service and ConfigMap per pool, rendered
// by a NodeDriver per implementation. Rendering, admission (Admit), topology
// (Peers), phase (PhaseOf) and the release rule (Releasable) are pure; the
// reconcilers are the thin I/O around them.
//
// The fence that keeps a pool to one forger follows the contract in
// models/specs/pool-forging/README.md ("Operator fencing contract"):
//
//   - one StatefulSet and pod name per PoolID (Name), whatever the driver or
//     digest, and the create of that name is the acquire;
//   - the fence finalizer is on the keyed pod template and comes off, by a
//     UID-preconditioned patch, only when the kubelet reports every
//     container terminated or the pod's Node is gone (Release);
//   - a roll updates the StatefulSet in place and a switch deletes it in the
//     foreground before recreating it under the same name;
//   - a scenario duplicate has its own name and its own Secret.
//
// The opcert is the engine's: the operator mounts the keys Secret read-only
// and never writes it, and a keys gate keeps a pod pending while the Secret
// is blank.
//
//	flowchart LR
//	  engine -- writes spec, Secret --> api[(API server)]
//	  api -- informer --> pools[Pools]
//	  pools -- Render via NodeDriver --> api
//	  api -- StatefulSet controller --> pod[keyed pod]
//	  pod -- kubelet status --> api
//	  api -- live read --> release[Release]
//	  release -- finalizer patch, uid + rv --> api
//	  api -- informer --> networks[Networks]
package operator
