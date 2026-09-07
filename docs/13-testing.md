# 13 · Testing

Two rules govern everything below.

> **Assert the exact code.** `require.Equal(t, policy.S5, verdict.FailedRule)`, never
> `require.Error(t, err)`. A test that only knows something failed cannot tell you that it failed
> for the wrong reason.

> **Test names are sentences.** `TestAChallengeIsNeverSignedTwice`, not `TestSign2`. The name is
> the specification, and a failing test should read like a claim being refuted.

## What is worth testing here

This system's correctness lives in three places, and they need three different kinds of test.

| Where | Kind | Why |
|---|---|---|
| The eight rules | Golden vectors — pure, in microseconds | They are the product. Every boundary must be pinned |
| The database's refusals | Integration, against a real server | A constraint nobody has watched reject something is a comment |
| The architecture's promises | Static analysis over the real package graph | "No outbound HTTP in the signing path" is not checkable by reading a diff |

## Golden vectors for S0–S7

`internal/domain/policy` is pure and takes `now` as a parameter, so a test case is a JSON file and
the whole suite runs in microseconds.

**Eight files, at least three cases each: pass, fail, boundary.**

```json
{ "rule": "S5", "code": "BUDGET_EXHAUSTED",
  "cases": [
    { "name": "pass · comfortably inside the remaining budget",
      "now": "2026-09-04T14:00:00Z",
      "snapshot": { "network":"sandbox", "state":"active", "cap_base":"10.000000",
                    "drawn_onchain_base":"2.000000", "reserved_base":"0.500000" },
      "request":  { "amount":"0.310000" },
      "expect":   { "verdict":"allowed" } },

    { "name": "boundary · exactly the remaining budget is ALLOWED",
      "snapshot": { "cap_base":"10.000000","drawn_onchain_base":"9.500000",
                    "reserved_base":"0.190000" },
      "request":  { "amount":"0.310000" },
      "expect":   { "verdict":"allowed" } },

    { "name": "fail · one micro-USDC over the remaining budget",
      "snapshot": { "cap_base":"10.000000","drawn_onchain_base":"9.500000",
                    "reserved_base":"0.190001" },
      "request":  { "amount":"0.310000" },
      "expect":   { "verdict":"blocked","failed_rule":"S5","code":"BUDGET_EXHAUSTED" } } ] }
```

### The boundaries that must be pinned

The interesting cases are not pass and fail. They are the edges, and the ones that catch a
half-implemented check.

| Rule | Cases beyond the obvious |
|---|---|
| **S0** | Correct key prefix but the challenge's **mint** belongs to the other network — the case that catches a check that only looked at the prefix. And an agent identifier whose prefix disagrees with its `network` field, which must be impossible and is asserted to panic in development |
| **S1** | `expiry_ts` **exactly equal to `now`** is expired — `>=`, not `>`. A snapshot 61 seconds old fails closed and forces a refresh |
| **S2** | A host that is on the list but a `payTo` that differs — checking only the host is wrong. A **subdomain** of an allowed host is **not** allowed, which is the one-host rule of P3. `allow_all` passes but returns a `wildcard` warning |
| **S3** | Exactly `per_tx_max` is allowed; one micro-USDC more is not |
| **S4** | An entry **exactly 600 seconds old is outside** the window. A sum exactly equal to the maximum is allowed |
| **S5** | Above |
| **S6** | `scheme: "upto"` is refused. A missing recipient token account is refused with `details` naming the account |
| **S7** | Killed **and** another rule failing — asserts **which** code comes back, because the evaluation order is part of the contract ([04-policy-engine.md](04-policy-engine.md)) |

And a meta-test:

```go
func TestEveryRuleHasAtLeastThreeVectorsAndOneBoundary(t *testing.T)
```

It walks the directory and fails if a rule is under-covered. **A vector nobody wrote is a coverage
gap nobody can see**, and the eight files are the most valuable thing in the repository.

### The S4 boundary is tested twice

Once against the pure engine in Go, and once against the real server through the
compare-and-swap's `$filter` aggregation. The two implementations of the same window can disagree,
and if they ever do, **the server is right and the engine is the bug** — because the server is
what actually admits the payment.

## The sixteen refusals

`leashctl verify-constraints`, against a throwaway server, as the application role. Each line is a
write the database **must** reject.

```
must refuse   a mainnet payment referencing a sandbox agent        → DocumentFailedValidation
must refuse   updating agents.network                              → DocumentFailedValidation
must refuse   updating agents._id                                  → ImmutableField
must refuse   drawn_onchain_base > cap_base                        → DocumentFailedValidation
must refuse   drawn + reserved > cap                               → DocumentFailedValidation
must refuse   a second live allowance for one (agent, mint)        → DuplicateKey
must refuse   a second sign_request for one challenge_hash         → DuplicateKey
must refuse   a second payment for one sign_request                → DuplicateKey
must refuse   a second payment with the same signature             → DuplicateKey
must refuse   a second handshake_event for one (payment, phase)    → DuplicateKey
must refuse   a `confirmed` phase with no read_back_slot           → DocumentFailedValidation
must refuse   a `confirmed` phase whose writer is the signer       → DocumentFailedValidation
must refuse   amount_base = 0                                      → DocumentFailedValidation
must refuse   amount_base as a double                              → DocumentFailedValidation
must refuse   amount_base as an untyped integer literal (int32)    → DocumentFailedValidation
must refuse   open_key present while state = revoked               → DocumentFailedValidation
```

Three of those deserve a note.

**`drawn + reserved > cap`** is the budget invariant itself. It means the signer's admission
filter cannot over-admit even if the filter is wrong, because the resulting document would be
refused. It is the highest-value line in the schema.

**A `confirmed` phase written by the signer** is invariant I4 as a database constraint rather than
a code review. There is no way to write it from the wrong component.

**An untyped integer literal** is the I6 trap made into a test. `bson.M{"amount_base": 5}` is a Go
`int`, and how the driver encodes that has varied between versions. The schema's `bsonType:"long"`
refuses it regardless, which is why the guarantee lives in the database and not in the Go type.

### And the privileges

`leashctl verify-privileges`, a separate command because it needs a different connection.

```
as leash_app:  updateOne(sign_requests)                  → Unauthorized
as leash_app:  deleteOne(sign_requests)                  → Unauthorized
as leash_app:  updateOne(handshake_events)               → Unauthorized
as leash_app:  deleteOne(policy_revisions)               → Unauthorized
as leash_app:  deleteOne(payments)                       → Unauthorized
as leash_app:  updateOne(payments)                       → OK — the state machine needs it
as leash_app:  insert with bypassDocumentValidation      → Unauthorized
assert:        the role document contains no `bypassDocumentValidation` action
```

The last line is the one the whole audit trail rests on ([12-security.md](12-security.md)).

### Both commands self-test, and the self-test runs first

Each ships with `--selftest`, which plants every violation against a deliberately broken schema
and asserts the checker catches it.

**This is not ceremony.** The sibling project's equivalent gate once passed everything because a
regex error made `grep` exit non-zero — which reads identically to "no match". A gate nobody has
watched reject something is a comment with a shell prompt in front of it.

## Migrations, proved reversible

```
make verify-migrations
  1. start a throwaway single-node replica set; wait for isWritablePrimary TWICE running
  2. apply every Up, in order
  3. snapshot: listCollections WITH ITS OPTIONS (validators included) + listIndexes,
     canonicalised to sorted JSON
  4. apply every Down, in reverse
  5. assert nothing of ours survives — not a collection, not an index, not a validator
  6. apply every Up again
  7. assert the new snapshot is BYTE-IDENTICAL to the one from step 3
```

Step 5 catches the classic bug: a `Down` that forgets something, which breaks the *next* deploy
under pressure rather than this one.

**Step 7 is the addition, and it is specific to this stack.** It catches a `Down` that drops
something the `Up` creates as a side effect, so the second `Up` produces a *slightly different*
schema. That is a live hazard in MongoDB because `createCollection` with a validator and `collMod`
are different operations that can leave different-looking option documents behind.

Step 1's doubled check is also not superstition. `rs.initiate()` returns before the node has been
elected, so the first `w:majority` write fails with `NotWritablePrimary`. Waiting for two
consecutive successes is what makes the harness reliable, and the comment in the code says so.

## Architecture checks

Two runtimes, two checkers, the same rules. Both self-tested, and both run their self-test first.

**Go — `leashctl verify-architecture`**, over the real package graph with `go/packages` and the
real type graph with `go/types`. Not a grep: greps miss dot-imports, aliases and blank imports.

| Rule | Catches |
|---|---|
| `domain/...` imports only the standard library | The day someone needs "just one" driver type inside a rule |
| Only `store`, `migrate` and `leashctl` import the driver | Ad-hoc queries growing inside handlers |
| **No driver type in `store`'s exported API**, recursively through struct fields | The real failure: a `store.Coll(name) *mongo.Collection` helper, after which the boundary is decorative while a grep still passes |
| `cmd/leash-indexer` does not reach `internal/kms` transitively | Only the signer holds keys |
| `signer/hot` does not directly import `net/http`, `chain/rpc`, `alerting` | An outbound call creeping into the 300 ms path |
| `indexer` does not import a signing function | I4 — repair by reading, never by re-signing |
| Every network-scoped store method takes `network.Network` | I8, made mechanical |
| No `float64`, `ParseFloat` or `map[string]any` decode in a money path | I6 |

**TypeScript — `dependency-cruiser` plus an ESLint rule.**

| Rule | Catches |
|---|---|
| `lib/domain/**` imports nothing but itself and the standard library | The same erosion, from the other direction |
| Only `lib/store/**` imports `mongodb`, and **`app/**` never does** | The most likely accident, given how easy a route handler makes it |
| **Nothing under `frontend/website` imports an AWS KMS client** | I1 on the TypeScript side. There is no key material in this deployable, and this is what says so |
| `lib/store` exports no `Collection` or `Db` type | The driver-leak rule again |
| Every route handler's first statement is `await requireOwner(req)` | Auth that fails closed — a middleware you forgot to attach fails open |
| `lib/domain/money.ts` has no `number` arithmetic and no `parseFloat` | I6 |
| No `NEXT_PUBLIC_*` variable exists | Everything read server-side at runtime |

### Contract parity between the runtimes

`contracts/` is committed and language-neutral, and both sides are checked against it —
`leashctl verify-contracts` on the Go side, a Vitest suite on the TypeScript side.

| File | Both runtimes must agree on |
|---|---|
| `error-codes.json` | Every code, its status, and its `retriable` flag ([11-errors-and-codes.md](11-errors-and-codes.md)) |
| `endpoints.json` | Which runtime serves each path, its method, its statuses, its auth |
| `policy-vectors/*.json` | S0–S7. Executed by the Go engine **and** by the TypeScript renderer of blocked verdicts |
| `money-vectors.json` | Parsing and formatting, including values that break a JavaScript `number` |

A code added on one side and not the other fails the build. This is the mechanism that replaces
the single shared `packages/core` the deck assumed.

### The boundary that is not a lint rule

*"The browser cannot reach the signer"* is not expressible in either checker, because the browser
is not in the build graph. It is a deployment fact: the signer has no public route from the
dashboard's origin, its ingress accepts only agent-key authentication, and its CORS configuration
does not include the dashboard. **The post-deploy smoke test asserts it** by attempting a
browser-shaped request and requiring it to fail.

## Integration tests

`internal/store/mongotest` starts `mongo:8.0` as a single-node replica set, waits for a writable
primary twice, applies the migrations, and hands back a store on a per-test database that is
dropped afterwards. Skipped under `-short`; a service container in CI.

The ones that earn their keep:

| Test | Proves |
|---|---|
| `TestTwoRequestsRacingForTheLastCentAdmitExactlyOne` | 64 goroutines, one payment's worth of budget. Exactly one success, 63 × `BUDGET_EXHAUSTED`, and `drawn + reserved ≤ cap` afterwards |
| `TestTheSameChallengeTwiceProducesOneSignature` | I7, at the storage layer. This is also evidence we present |
| `TestACrashBetweenSigningAndRecordingReplaysByteIdentically` | The pinned blockhash and deterministic Ed25519 ([06-signer.md](06-signer.md)) |
| `TestReservedBaseConvergesAfterALeak` | Leak a reservation, run `allowance-refresh`, assert repair **and a warning carrying the delta** |
| `TestAVelocityEntryExactlyAtTheWindowEdgeIsOutside` | The `$filter` boundary against the real server |
| `TestNoOutboundHttpDuringSigning` | A panicking transport, 1,000 signatures, nothing panicked |
| `TestAKilledAgentIsRefusedWithinTheChangeStreamLatency` | S7's "instantly", measured rather than assumed |

## The sandbox end-to-end run

`make e2e-sandbox`, in CI on every run. The deck makes it a merge gate and so do we.

```
create an agent  →  delegate  →  read back to active  →  pay through demo-402
                 →  assert the six-phase timeline     →  revoke  →  read back to revoked
```

This is the only test that would catch a wrong account layout in the adapter, which is why **any
pull request touching `internal/chain` must attach its results** ([15-roadmap.md](15-roadmap.md)).

## The four pieces of evidence

Demonstrable on demand, three of them negative claims. Scripted, so they can be run in front of
somebody.

| Claim | The proof |
|---|---|
| **Hard enforcement is real** | Attempt a draw beyond the cap. **The chain** rejects it — not our policy engine. Visible in an explorer |
| **We never pay twice** | Submit the same challenge twice. Exactly one signature exists |
| **We are non-custodial** | No code path receives an owner key — the self-tested gate. Every treasury transaction has the owner's wallet as signer |
| **The networks are isolated** | An `lk_test_` key with a mainnet challenge → `403 NETWORK_MISMATCH`, and zero transactions anywhere |

The first is the only one that demonstrates the hard tier exists at all, and it is the one to run
first for anyone sceptical that the cap is more than a policy check.

## Frontend

Vitest with jsdom and Testing Library for the units — the money formatter, the status mapping, the
tier renderer. Playwright for the flows the deck names: onboarding through to a real test payment,
the blocked-to-recovered loop, and the kill switch.

Two frontend tests are not optional, because both guard a rule from
[10-ui-spec.md](10-ui-spec.md):

- **No amount is ever a JavaScript `number`.** A lint rule plus a test that feeds
  `"9007199254740993.000001"` through the formatter and gets it back unchanged.
- **A confirmed state never renders without a slot.** The component throws in development if asked
  to, rather than silently omitting it.

## CI

| Job | Proves |
|---|---|
| `arch-go` | `verify-architecture --selftest`, **then** `verify-architecture` |
| `arch-ts` | The dependency-cruiser self-test, then the real run |
| `no-custody` | The gate's self-test, then the gate; no committed secrets; **and that `frontend/website` has no KMS client in its lockfile resolution** |
| `backend` | `gofmt -l`, `go vet`, `golangci-lint`, `go test ./...` |
| `schema` | `verify-migrations`; `verify-constraints --selftest` then the real run; `verify-privileges` |
| `vectors` | S0–S7 standalone **in both runtimes**, so a failure names the rule and the language |
| `contracts` | `verify-contracts` and the Vitest suite agree on codes, endpoints, vectors and money |
| `docs` | Every code here matches the golden file; every documented endpoint is routed by the runtime that claims it, and every routed endpoint documented; every invariant is referenced |
| `e2e-sandbox` | The full loop against Surfnet, across all four deployables |
| `web` | Typecheck, unit tests, Playwright, build |

The `docs` job is what keeps this documentation from becoming fiction. Prose that is not checked
against code describes a system that used to exist.
