# 11 · Errors and codes

**The codes in this document are frozen.** They are an interface: an agent SDK branches on them,
an operator runbook maps them to actions, and the dashboard's copy table is keyed by them.
Renaming one is a breaking change to somebody else's software.

CI compares this table against `internal/domain/fault/testdata/error-codes.golden`. A code added
in Go and not documented here fails the build, and so does the reverse.

## The envelope

Always exactly one top-level key, from every endpoint of both services.

```json
{
  "error": {
    "code": "ENDPOINT_NOT_ALLOWED",
    "message": "api.unknown.xyz is not on this agent's allow list",
    "field": "host",
    "details": { "rule": "S2", "tier": "signer" },
    "retriable": false,
    "trace_id": "01J8XKQ2M4Z7YB3F9C5R6D8W1H"
  }
}
```

| Member | Rule |
|---|---|
| `code` | Stable. **Never renamed.** Screaming snake case |
| `message` | For a person. May be reworded, may be translated. Never parsed |
| `field` | The input to blame, so the interface can highlight it rather than showing a generic toast. Omitted when there is no input at fault |
| `details` | Structured, optional |
| `retriable` | **Always present, even when `false`.** An SDK must not have to infer it from the code |
| `trace_id` | Joins the response, the log line, and `handshake_events.detail`. Give it to support and they can find the request |

**Absent optional members are omitted, never `null`.** In Go this means no `omitempty` on
`retriable` — the zero value is meaningful and must be transmitted — and `omitempty` on
everything else.

## The policy codes — S0 to S7

Returned by `POST /v1/sign` with **403**. These are the ones an agent SDK actually branches on.

| Code | Rule | Tier | Retriable | Means |
|---|---|---|---|---|
| `NETWORK_MISMATCH` | S0 | signer | no | The key's network, or the challenge's, does not match the agent's. **Nothing was signed and no transaction exists** |
| `ALLOWANCE_INACTIVE` | S1 | chain | no | Expired, revoked, or never activated |
| `ENDPOINT_NOT_ALLOWED` | S2 | signer | no | The host or `payTo` is not on the allow-list. The owner can fix this in one tap |
| `PER_TX_LIMIT` | S3 | signer | no | The amount exceeds the per-payment maximum |
| `VELOCITY_LIMIT` | S4 | signer | **yes** | The ten-minute window is full. It will not be, later |
| `BUDGET_EXHAUSTED` | S5 | chain | no | `remaining < amount`. Also what the loser of the FIFO race receives |
| `UNSUPPORTED_TERMS` | S6 | signer | no | Wrong mint, unsupported scheme, or a missing recipient token account. `details` says which |
| `KILLED` | S7 | signer | no | The kill switch is on. The agent should stop, not retry |

`VELOCITY_LIMIT` is the only retriable one, and the distinction matters: a velocity block clears
on its own, so an SDK may back off and try again. Every other block needs a human to change
something, and an agent that retries into them is just generating noise.

**The `details` on a block carries the evaluation record:**

```json
"details": {
  "rule": "S2", "tier": "signer", "checks_total": 8,
  "checks": [ { "rule": "S0", "tier": "signer",  "result": "pass" },
              { "rule": "S1", "tier": "onchain", "result": "pass" },
              { "rule": "S2", "tier": "signer",  "result": "fail" } ]
}
```

That array is what the interface renders as phase 2 of the timeline, and its length is where the
check count comes from ([10-ui-spec.md](10-ui-spec.md)).

## Validation — 400 and 422

`400` is a malformed request. `422` is a well-formed request that a domain rule refused.

| Code | Status | Field | Means |
|---|---|---|---|
| `INVALID_AMOUNT` | 422 | `amount` | Not a decimal string, more than six places, negative, zero, or above the maximum. **A JSON number gets this too** (I6) |
| `INVALID_HOST` | 422 | `host` | Not a hostname, or a wildcard where one is not permitted |
| `INVALID_WALLET` | 422 | `wallet` | Not a base58 Solana address |
| `INVALID_NETWORK` | 422 | `network` | Not `sandbox` or `mainnet` |
| `UNSUPPORTED_CHALLENGE_VERSION` | 422 | — | An x402 version whose canonical form we do not know. **We refuse rather than guess**, because guessing changes the hash and the hash is the idempotency key |
| `TOO_MANY_HOSTS` | 422 | `host` | The one-host rule of `PATCH …/policy/allow-hosts`. A wildcard, a list, or a parent domain |
| `MAINNET_ONLY_OPERATION` | 422 | — | e.g. the faucet, called against mainnet. Nonsensical rather than forbidden |
| `MISSING_IDEMPOTENCY_KEY` | 400 | — | A creating POST without the header |
| `MALFORMED_REQUEST` | 400 | varies | Schema failure |

`UNSUPPORTED_CHALLENGE_VERSION` is worth dwelling on. The deck's open question `[Q4]` is which
fields belong in the canonical hash across x402 versions. Until that is answered with real
vectors, an unknown version is refused rather than canonicalised by guesswork: a wrong guess
produces a *different hash for the same challenge*, which defeats I7 and permits a double
payment. **Refusing is the safe direction.**

## Conflict — 409

| Code | Retriable | Means |
|---|---|---|
| `IDEMPOTENCY_CONFLICT` | no | The same `Idempotency-Key` with a **different** body. The key is spent |
| `SIGN_IN_PROGRESS` | **yes** | The same challenge is being signed right now. Wait and retry **with the same challenge** |
| `ALLOWANCE_ALREADY_LIVE` | no | An agent already has a live allowance for that mint — `one_active_allowance` |
| `INTENT_EXPIRED` | **yes** | The blockhash aged out before the wallet signed. Request a new intent |

**`SIGN_IN_PROGRESS` must never be answered by generating a new challenge.** Retrying with the
same one returns the original signature; retrying with a fresh one creates a second payment for
the same API call. The message says so, and the SDK documentation repeats it.

## Authentication — 401 and 403

| Code | Status | Means |
|---|---|---|
| `UNAUTHENTICATED` | 401 | No session, or an expired one |
| `INVALID_SIGNATURE` | 401 | The SIWS signature did not verify |
| `NONCE_ALREADY_USED` | 401 | Replay. The nonce was consumed |
| `NONCE_EXPIRED` | 401 | Older than five minutes |
| `KEY_REVOKED` | 401 | The agent key was rotated or the agent deleted |
| `FORBIDDEN` | 403 | Authenticated, but not for this resource |

**A resource belonging to another organisation returns `404`, not `403`.** A `403` confirms the
identifier exists, which is an enumeration oracle. The only exception is the policy verdict on
`/v1/sign`, where `403` is the meaningful answer and there is nothing to enumerate.

Every sign-in failure returns one identical `401` to the client, and the actual reason is always
logged.

## Storage and dependencies — 404, 429, 503

| Code | Status | Retriable | Means |
|---|---|---|---|
| `NOT_FOUND` | 404 | no | No such resource, or not yours |
| `RATE_LIMITED` | 429 | **yes** | The faucet guard. `details.retry_after_s` |
| `CHAIN_UNREACHABLE` | 503 | **yes** | Primary and fallback RPC both failed |
| `STORE_UNAVAILABLE` | 503 | **yes** | No primary, or a write concern timeout |
| `INTERNAL` | 500 | no | An untyped error escaped a handler. A bug, logged with the real cause |

`INTERNAL` deliberately says nothing about internals. The real error goes to the log with the
`trace_id`, and the client gets a code and that identifier.

## Boot errors

These never reach the wire. The process prints to stderr **and** logs, then exits non-zero.

| Code | Means |
|---|---|
| `CONFIG_MISSING` | A required variable is unset. The message names **every** missing one at once, not the first |
| `CONFIG_INVALID` | Present but unparseable |
| `CONFIG_HALF_CONFIGURED` | A subsystem partly set up — mainnet in the network list with no `RPC_MAINNET_URL`, or alerts enabled with no bot token |
| `MIGRATION_FAILED` | A migration errored, or a checksum no longer matches its applied version |
| `SCHEMA_ASSERTION_FAILED` | An expected index or validator is missing. Usually a hand-edited database |
| `PRIVILEGE_ASSERTION_FAILED` | The application role has gained `bypassDocumentValidation`, or `update` on an append-only collection |

The last two run **before the port binds**. A service that starts serving and then discovers its
schema is wrong reports itself healthy while being useless, and a load balancer believes it.

`CONFIG_HALF_CONFIGURED` exists because the alternative — booting with mainnet enabled and
silently pointing at a default endpoint — is the failure mode invariant I8 exists to prevent.

## Rules for changing this table

1. **A code is never renamed.** If the meaning changes, add a new code and deprecate the old one
   in this document.
2. **A code is never reused** for a different condition.
3. **Adding one** means: the Go constant, this table, and the golden file. CI fails until all
   three agree.
4. **`retriable` is part of the contract.** Flipping it changes SDK behaviour, so treat it as a
   rename.
5. **The message may change freely.** It is for a person and is never parsed. If you find yourself
   wanting to parse one, the information belongs in `details`.
