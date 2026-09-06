# Results, evidence and durability

Each run uses separate private runtime and persistent evidence directories.
Directories require operator ownership and 0700; regular evidence/config files
require 0600. Symlink ancestors, foreign ownership and writable shared ancestors
are rejected except a root-owned sticky temporary ancestor. Same-UID malicious
replacement is outside the stated trust model.

`manifest.json` persists lifecycle stage, host boot ID, UID, safe random run ID,
created path names, process PID/start identity, executable and artifact identities,
configuration/input hashes and cleanup status. Records are written around side
effects, but a crash can occur before the next record. A BOOTING record without
process identity on the same boot is ambiguous and requires manual review.

Files:

| File | Meaning |
| --- | --- |
| scenario.input.json | Exact original JSON input bytes |
| scenario.resolved.json | Validated/defaulted plan with UTC targets |
| manifest.json | Ownership and current lifecycle stage |
| events.ndjson | Ordered events with stage, sequence, elapsed time and host UTC |
| clock-observations.ndjson | Explicitly labelled host and guest clock records |
| actions.ndjson | Completed/failed steps and bounded action diagnostics |
| firecracker.stdout.log / firecracker.stderr.log | Diagnostic console/VMM streams, never command results |
| report.json | Atomic final report, published after cleanup is known |

The default combined limit is 8 MiB excluding private images and their launch
configuration. Within evidence, reserve 2 MiB for the final report and 64 KiB for
ownership metadata. Remaining bytes are shared by inputs, observations, events
and logs; step plus assertion detail is additionally bounded to 1 MiB so it fits
the final reserve. JSON control frames are at most 1 MiB, each action stream is
at most 64 KiB, and events are limited to 4096 records. These are conservative
design limits, not measured throughput claims.

Overflow cancels execution and records an explicit cause. Output collectors keep
draining/discarding until termination to avoid deadlocking a full pipe. Required
truncated JSON cannot pass. Step errors can retain typed partial details including
clock samples and truncation flags; these are not successful assertion inputs.

Independent report dimensions:

| Dimension | Values |
| --- | --- |
| execution_status | COMPLETED, ERROR, CANCELLED, INTERRUPTED |
| assertion_status | PASS, FAIL, NOT_EVALUATED |
| cleanup_status | COMPLETE, INCOMPLETE |

Only COMPLETED + PASS + COMPLETE permits exit 0. Earlier completed assertion
results survive a later execution failure, while overall assertions remain
NOT_EVALUATED for an unfinished experiment. Automatic exit-code assertions use
reserved `auto:STEP:exit_code` IDs. Application exit mismatch is an assertion
failure; missing/malformed output, timeout or process/transport failure is ERROR.

Atomic publication uses a temporary file in the destination directory, fsync,
rename and Linux directory fsync. Compact final JSON preserves byte accounting.
A renamed file is not a promise to survive arbitrary storage/controller faults.
Storage failures remain execution errors; the CLI does not announce saved evidence
when final publication failed.

SIGKILL/power loss can leave partial NDJSON and no final report. An unterminated
last JSON record remains partial even if its syntax is valid. The reader preserves
complete records without silently treating the tail as completed evidence. Report
inspection without a final file is INTERRUPTED. Recovery never promotes partial
assertions into a passing experiment or resumes the interrupted business action.
