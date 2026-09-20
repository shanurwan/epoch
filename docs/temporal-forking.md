# Temporal forking

Epoch's project thesis is **Temporal Forking Infrastructure for Deterministic
Batch & Expiry Testing**. “Temporal forking” names the testing model: evaluate
the same declared software and workload around meaningful wall-clock boundaries
without changing the host clock.

## What exists now

Each `epoch run` currently performs one independent temporal execution:

1. validate one portable scenario and its locally prepared image declaration;
2. make a full private byte copy of the declared root filesystem;
3. cold-boot one Firecracker microVM;
4. establish and verify an actual guest wall-clock target;
5. execute only declared non-root workload actions;
6. record observations and evaluate typed assertions; and
7. clean up owned runtime resources while retaining persistent evidence.

Running two scenarios against the same image ID gives equivalent declared base
image and software inputs, subject to hash verification. It does not give identical
memory, device, scheduler, entropy, or boot state. Epoch currently has no
Firecracker snapshot creation or restore path, no memory-snapshot branching, and
no multi-branch orchestrator. “Snapshot fork” would therefore be inaccurate.

Useful precise terms for the current implementation are **temporal execution**,
**temporal branch**, **equivalent declared inputs**, and, when the operator reruns
the same declaration, **temporal replay**. A replay is a new cold boot and private
disk copy. The documented authority scenarios each produced one normalized
semantic result across 20 executions on the recorded host; this does not prove
general deterministic replay.

## Roadmap

| Phase | Meaning | Status |
| --- | --- | --- |
| 1 | Controlled independent temporal executions from equivalent declared image inputs | Implemented; documented clock/expiry paths hardware-verified on one reference host |
| 2 | Snapshot-backed temporal forks from a captured memory/device state | Future; not implemented |
| 3 | Coordinated multi-branch experiments and branch comparison | Future; not implemented |

Snapshot support, if added, must preserve the existing safety and evidence model:
no host clock changes, no implicit action replay, explicit artifact identity,
separate runtime/evidence roots, and independent execution/assertion/cleanup
outcomes. Snapshot restore alone would not prove deterministic execution.

## Why the model is general-purpose

The core operates on workload actions and JSON observations rather than business
concepts. A scheduled-batch workload can observe whether a job became eligible; a
subscription workload can observe access state; a certificate workload can observe
validity; and the included authority-expiry workload can observe an authorization
decision. None requires a batch, subscription, certificate, or agent type in Epoch
core.
