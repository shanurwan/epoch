# Incident-response agent reference application

Epoch remains **Temporal Forking Infrastructure for Deterministic Batch & Expiry Testing**. This directory is one isolated reference application that exercises a stateful expiry boundary. Removing it leaves Epoch core, its scenario model, guest protocol, assertions, evidence, and other workloads intact.

```text
Epoch generic action.exec boundary
              |
              v
deterministic incident-response-agent
              |
              | official MCP Go SDK over stdio
              v
        incident-ops-mcp
              |
              | Unix-domain socket only
              v
          PostgreSQL 16
```

The agent, MCP server, authority validation, database, audit timestamps, and simulated side effect all run inside the Firecracker guest. No language model, container runtime, cluster, host database, or runtime Internet access is involved.

## Behavior

The deterministic agent discovers four real MCP tools and follows a fixed workflow:

1. `incident_list` selects the lexicographically first active critical incident.
2. `incident_inspect` selects the lexicographically first associated unhealthy worker.
3. `worker_status` confirms current PostgreSQL state.
4. `worker_restart` attempts the simulated privileged side effect.
5. `worker_status` reads the resulting database state.

The synthetic fixture is `INC-2026-001`, service `payments-api`, and worker `payments-worker-17`. A restart never touches an operating-system service. It is a serializable database transaction that changes `health` from `unhealthy` to `healthy` and increments `restart_count` exactly once.

## Delegated authority

`incident-authority-issuer` creates a new in-memory Ed25519 keypair and signed JWT for each action. The private key never leaves the issuer process. The MCP server receives only the public key; the agent receives the token. Neither is written to PostgreSQL or emitted in workload evidence.

The signed claims include `sub`, `nbf`, `exp`, `jti`, `scope`, and `resource`. Signature verification is followed by explicit typed checks. The validity interval is half-open:

```text
not_before <= now < expires_at
```

Thus `now == expires_at` returns `DENY_EXPIRED`. Other fail-closed outcomes include `DENY_NOT_YET_VALID`, `DENY_SCOPE_MISMATCH`, `DENY_RESOURCE_MISMATCH`, `DENY_SUBJECT_MISMATCH`, `DENY_INVALID_SIGNATURE`, `DENY_MALFORMED_AUTHORITY`, and `DENY_REPLAYED_AUTHORITY`.

`worker_restart` reads the actual guest wall clock and validates at request handling. After any requested real-time delay, it reads the guest wall clock again and revalidates immediately before entering the PostgreSQL transaction. The transaction consumes `jti`, locks and updates the worker row, and inserts the successful audit record atomically. A failed initial or execution check inserts a denial audit row without updating the worker.

## PostgreSQL lifecycle

The demo image adds a dedicated locked `postgres` account (UID/GID 10002). PostgreSQL listens only on `/run/postgresql`; TCP is disabled. Peer mapping permits the constrained Epoch workload account to connect only as the non-superuser `incident_app` role. PostgreSQL starts and the embedded schema/seed transaction completes before `epoch-agent` becomes ready.

Every boot resets the synthetic tables to the declared baseline (`health=unhealthy`, `restart_count=0`). Epoch also creates a private writable disk copy per run, so independent executions do not share database mutation.

The preparation scripts do not download packages. Supply a local Ubuntu 24.04 userspace, the complete local amd64/all `.deb` closure for PostgreSQL 16 and its runtime libraries, and existing local Epoch inputs.

## Build on the Rocky Linux operator host

Run from an exact Epoch checkout. These are explicit preparation operations; none is performed by `epoch run`.

```bash
cd /home/wan/epoch
test -z "$(git status --porcelain)"
SOURCE_ID=$(git rev-parse HEAD)
DEMO_BIN_DIR="/home/wan/epoch-lab/build/incident-response-agent-$SOURCE_ID"
DEMO_IMAGE_DIR="/home/wan/epoch-lab/images/incident-response-agent-$SOURCE_ID"

(cd demos/incident-response-agent && GOTOOLCHAIN=local go mod download)

bash demos/incident-response-agent/scripts/build-binaries.sh \
  "$DEMO_BIN_DIR"

sudo env PATH=/usr/sbin:/usr/bin:/sbin:/bin \
  bash demos/incident-response-agent/scripts/prepare-postgres-userspace.sh --apply \
  /home/wan/epoch-lab/artifacts/epoch-candidates-20260907/ubuntu-24.04.squashfs \
  /home/wan/epoch-lab/artifacts/postgresql-16-debs \
  /home/wan/epoch-lab/prepared/ubuntu-24.04-postgresql-16

sudo env PATH=/usr/sbin:/usr/bin:/sbin:/bin \
  bash demos/incident-response-agent/scripts/build-guest-image.sh --apply \
  /home/wan/epoch-lab/prepared/ubuntu-24.04-postgresql-16 \
  /home/wan/epoch-lab/artifacts/epoch-candidates-20260907/vmlinux-6.1.155 \
  /home/wan/epoch/bin/epoch-agent \
  "$DEMO_BIN_DIR" \
  "$DEMO_IMAGE_DIR" \
  "source $SOURCE_ID; local Ubuntu 24.04 plus recorded offline PostgreSQL 16 package closure" \
  3072
```

Use a new output directory for each build. Copy the resulting image manifest, kernel, rootfs, workload manifest, and `demo-provenance.json` into the local artifact-manifest layout already documented by Epoch. Do not commit the image or package files.

## Validate and run

The local config must reference the prepared `epoch-incident-response-agent-v1` manifest. Validation hashes local artifacts but does not boot a VM.

```bash
bin/epoch validate scenarios/incident-response-mcp-before-expiry.json --config epoch.local.json
bin/epoch validate scenarios/incident-response-mcp-after-expiry.json --config epoch.local.json
bin/epoch validate scenarios/incident-response-mcp-toctou.json --config epoch.local.json

bin/epoch run scenarios/incident-response-mcp-before-expiry.json --config epoch.local.json --json
bin/epoch run scenarios/incident-response-mcp-after-expiry.json --config epoch.local.json --json
bin/epoch run scenarios/incident-response-mcp-toctou.json --config epoch.local.json --json
```

The before-expiry case expects `0 -> 1`; the after-expiry and real TOCTOU cases expect `0 -> 0`. TOCTOU starts the guest near `12:00:25Z`, waits 7000 ms inside `worker_restart`, and expects execution-time `DENY_EXPIRED` after the `12:00:30Z` boundary.

Inspect the database-backed worker transition and returned audit row retained in Epoch evidence:

```bash
bin/epoch report RUN_ID --config epoch.local.json --json |
  jq '.steps[] | select(.id == "agent-run") | .result.stdout_json |
      {worker_before, restart, worker_after}'
```

The report omits the JWT and signing key. The guest and its private disk are cleaned up after the run, so retained Epoch evidence—not a surviving database—is the post-run audit surface.

## Repeatability preparation

The harness runs normal foreground `epoch run` commands, retains every captured report, excludes volatile IDs/timestamps from semantic hashes, and records failures without deleting raw Epoch evidence:

```bash
bash demos/incident-response-agent/scripts/verify-reference-repeatability.sh --apply \
  /home/wan/epoch/bin/epoch \
  /home/wan/epoch/epoch.local.json \
  "/home/wan/epoch-lab/repetition/incident-response-$SOURCE_ID" \
  20
```

No repeatability or hardware result is claimed here. Guest boot, PostgreSQL startup, MCP stdio execution, real temporal-boundary behavior, and repetition remain unverified until the resulting hardware evidence demonstrates them.

## Development checks

```bash
cd demos/incident-response-agent
go test ./...
go vet ./...
go build ./...
go test -race ./...
```
