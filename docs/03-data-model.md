# 03 · Data model

Seventeen collections in MongoDB 8.0, on a replica set. Thirteen come from chapter 12 of the deck,
which specifies them as PostgreSQL tables; four are additions argued below.

This chapter is the one place where the stack decision has to be paid for honestly. The deck
pushes correctness into the database — `CHECK` constraints, partial unique indexes, and `REVOKE`
to make four tables append-only — and that instinct is right and is kept. What changes is the
mechanism, and in two places the mechanism is weaker. Those two places are named, not buried.

## Global rules

- **Replica set, always**, including on a laptop. Transactions and change streams both need one.
- Client settings: `w:majority`, `readConcern:majority`, `readPreference:primary`, retryable
  writes on. A money write acknowledged by one node and lost in a failover is a signature we
  handed out and cannot account for.
- **Every collection is created with `validationLevel:"strict"` and `validationAction:"error"`.**
  Strict validates the whole post-image on updates too, so `$set` and `$inc` are covered, not
  just inserts.
- **The validator is always `{$and:[{$jsonSchema:{…}},{$expr:{…}}]}`.** `$jsonSchema` carries
  types, requiredness and ranges. `$expr` carries the cross-field checks that JSON Schema cannot
  express, because JSON Schema has no way to reference a sibling property's value.
- **`additionalProperties:false` on every schema.** A mistyped field name in a `$set` becomes a
  write error rather than a silently orphaned key. This is the single most common way a MongoDB
  schema rots.
- **`bsonType:"long"` on every money field and every slot.** This, not the Go type, is the
  guarantee (I6): it holds regardless of driver version, shell session or future contributor.
- **`jsonb` becomes an embedded document plus a `raw` string** holding the canonical JSON text.
  Two reasons: BSON field names historically choke on `.` and `$`, and a challenge's fields are
  attacker-influenced; and `challenge_hash` is a hash over exactly that text, so storing the text
  is storing the thing that was hashed.
- **Timestamps are BSON dates — milliseconds**, where `timestamptz` gives microseconds. Ordering
  ties break on `_id`, which is why FIFO is defined against the ULID and not against a clock
  ([04-policy-engine.md](04-policy-engine.md)).

## Identifiers carry their network

The deck asks for `uuid` primary keys and a trigger forbidding `UPDATE agents.network`. We use a
prefixed ULID instead:

```
org_01J8XKQ2M4Z7YB3F9C5R6D8W1H
agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1H     agt_live_01J8XKQ2M4Z7YB3F9C5R6D8W1H
alw_test_…   pay_test_…   sgr_test_…   hse_…   rev_…   alr_…   job_…
tpl_research   tpl_scraper   tpl_blank
```

**MongoDB refuses any update that modifies `_id`.** That is a storage-engine guarantee, not a
convention. So the network component of an agent's identity is immutable by construction, which
is strictly stronger than the trigger the deck asked for. A validator then pins the redundant
`network` field to the prefix, so the field cannot drift from the identifier either.

Between the two, **I8 becomes structural rather than aspirational**. A mainnet `payments`
document cannot reference `agt_test_…`, because the validator computes both prefixes and compares
them — no `$lookup` required. And it is visible to the naked eye: you can spot a sandbox
identifier in a mainnet incident without running a query.

ULIDs are time-sortable and monotonic within a millisecond, which is what makes them usable as
the FIFO tie-break. They replace `bigserial` on `handshake_events` and `policy_revisions`, at a
cost noted in [§ What is weaker](#what-is-weaker).

## The enums

Four independent axes, and merging any two is a data-model bug ([02-invariants.md](02-invariants.md)).

```
network            sandbox · mainnet
allowance state    pending · active · exhausted · expired · revoked
payment state      signed · submitted · confirmed · failed · unknown
verdict            allowed · blocked
handshake phase    challenge_received · rules_evaluated · signed · replayed · broadcast · confirmed
```

They are `$jsonSchema` `enum` constraints rather than a database type. Adding a value is a
`collMod` in a migration, not an `ALTER TYPE`.

Two allowance states are **derived, never stored**:

- **`exhausted`** — the cap is fully drawn but the allowance is still live until it expires. The
  interface prompts a top-up.
- **`expiring-soon`** — under 24 hours left. Amber in the Expires column, and an alert if enabled.

Both are computed at query time from chain data. Storing them would create a second thing that
can be stale.

## The collections

| # | Collection | `_id` | Owner | Notes |
|---|---|---|---|---|
| 1 | `orgs` | `org_<ulid>` | API | `owner_wallet` unique |
| 2 | `templates` | `tpl_research` etc. | API | Seeded by migration 0011, read-only at runtime |
| 3 | `agents` | `agt_{test\|live}_<ulid>` | API | Network immutable via `_id` and `$expr` |
| 4 | `agent_keys` | = the agent's `_id` | **Signer** | 1:1, so `_id` *is* the reference |
| 5 | `allowances` | `alw_{test\|live}_<ulid>` | Indexer | Plus `reserved_base`, `vel[]`, `epoch`, `open_key`, `expiry_tier` |
| 6 | `policies` | = the agent's `_id` | API | 1:1 |
| 7 | `policy_revisions` | `rev_<ulid>` | API | **append-only** |
| 8 | `sign_requests` | `sgr_{test\|live}_<ulid>` | Signer | **append-only** |
| 9 | `payments` | `pay_{test\|live}_<ulid>` | Signer inserts, indexer transitions | **no delete** |
| 10 | `handshake_events` | `hse_<ulid>` | Signer 1–3, indexer 5–6 | **append-only** |
| 11 | `alerts` | `alr_<ulid>` | API | |
| 12 | `suppressions` | `{a:<agentID>, h:<host>}` | API | The composite primary key, exactly |
| 13 | `audit_jobs` | `job_<ulid>` | API | |
| +1 | `sign_claims` | = `challenge_hash` | Signer | Idempotency claiming — see below |
| +2 | `chain_cursors` | = the network | Indexer | One per network. How far the read-back has got, which endpoint answered, and the current lag |
| +3 | `siws_nonces` | = the nonce | API | TTL index. **Fills a gap in the deck** |
| +4 | `schema_migrations` | = the version | migrate | The ledger and the boot lock |

### The four additions

**`sign_claims`.** `sign_requests` is append-only and carries a *verdict* — which is not known at
the moment you must win the race against a concurrent retry. Trying to make one document be both
the race token and the immutable verdict record is where this design goes wrong. So the claim is
separate: `_id` is the `challenge_hash`, and inserting it is how a request wins.
`sign_requests.challenge_hash` stays unique as a second, independent guard.

**`siws_nonces`.** The deck specifies Sign-In With Solana and specifies no nonce store. Without
one, a signed sign-in message is replayable for as long as it exists — anyone who observes it can
re-present it. This is a genuine gap in the specification, not an artefact of MongoDB, and it is
registered as such in [16-deck-conformance.md](16-deck-conformance.md). A nonce is issued,
consumed once by deletion, and expires by TTL.

**`chain_cursors`.** One document per network, holding how far the read-back has got, which RPC
endpoint answered, and the current lag in seconds. It is what the interface's slot chip and
degraded banner read from. There is deliberately no global cursor: a single one would be the piece
of shared state capable of letting a sandbox read advance a mainnet position.

**`schema_migrations`.** The ledger, plus a lock document. See [§ Migrations](#migrations).

## Three collections in full

The rest follow the same pattern; these three carry the invariants.

### `agents`

```js
db.createCollection("agents", {
  validator: { $and: [
    { $jsonSchema: {
        bsonType: "object",
        required: ["_id","org_id","name","template_id","network","pubkey",
                   "idempotency_key","killed","created_at"],
        additionalProperties: false,
        properties: {
          _id:             { bsonType: "string",
                             pattern: "^agt_(test|live)_[0-9A-HJKMNP-TV-Z]{26}$" },
          org_id:          { bsonType: "string", pattern: "^org_[0-9A-HJKMNP-TV-Z]{26}$" },
          name:            { bsonType: "string", minLength: 1, maxLength: 64 },
          template_id:     { bsonType: "string", enum: ["research","scraper","blank"] },
          network:         { bsonType: "string", enum: ["sandbox","mainnet"] },
          pubkey:          { bsonType: "string", pattern: "^[1-9A-HJ-NP-Za-km-z]{32,44}$" },
          idempotency_key: { bsonType: "string", minLength: 8, maxLength: 128 },
          killed:          { bsonType: "bool" },
          killed_at:       { bsonType: ["date","null"] },
          created_at:      { bsonType: "date" }
        } } },

    // I8 · the trigger the deck asked for. `_id` is immutable at the storage engine;
    // this stops `network` from ever disagreeing with it.
    { $expr: { $eq: [
        { $arrayElemAt: [ { $split: ["$_id","_"] }, 1 ] },
        { $cond: [ { $eq: ["$network","sandbox"] }, "test", "live" ] } ] } }
  ]},
  validationLevel: "strict", validationAction: "error"
});

db.agents.createIndex({ pubkey: 1 },          { unique: true, name: "one_agent_per_pubkey" });
db.agents.createIndex({ idempotency_key: 1 }, { unique: true, name: "one_agent_per_idempotency_key" });
db.agents.createIndex({ org_id: 1, network: 1, created_at: -1 },
                                              { name: "agents_by_org_and_network" });
```

### `allowances`

The busiest document in the system, and the one the signing path contends on.

```js
db.createCollection("allowances", {
  validator: { $and: [
    { $jsonSchema: {
        bsonType: "object",
        required: ["_id","agent_id","network","onchain_addr","mint","cap_base",
                   "drawn_onchain_base","reserved_base","state","epoch",
                   "expiry_ts","expiry_tier","vel","created_at"],
        additionalProperties: false,
        properties: {
          _id:              { bsonType: "string",
                              pattern: "^alw_(test|live)_[0-9A-HJKMNP-TV-Z]{26}$" },
          agent_id:         { bsonType: "string",
                              pattern: "^agt_(test|live)_[0-9A-HJKMNP-TV-Z]{26}$" },
          network:          { bsonType: "string", enum: ["sandbox","mainnet"] },
          onchain_addr:     { bsonType: "string", minLength: 32, maxLength: 44 },
          mint:             { bsonType: "string", minLength: 32, maxLength: 44 },
          token_program_id: { bsonType: "string" },   // SPL vs Token-2022 — see ch. 08

          // I6 · long, never double, never int32. `minimum` keeps CHECK (> 0).
          // `maximum` is 10^15 µUSDC = $1B, so a $sum in the admission filter can
          // never reach the range where it promotes to a double.
          cap_base:           { bsonType: "long", minimum: 1, maximum: 1000000000000000 },
          drawn_onchain_base: { bsonType: "long", minimum: 0, maximum: 1000000000000000 },
          reserved_base:      { bsonType: "long", minimum: 0, maximum: 1000000000000000 },

          // I2 · every number the interface shows must name the slot it was read at.
          last_read_slot: { bsonType: ["long","null"] },
          last_read_at:   { bsonType: ["date","null"] },

          expiry_ts: { bsonType: ["date","null"] },
          // I3 · which tier enforces expiry is DATA, because the SPL adapter downgrades
          // it from chain to signer. The interface reads this, never a constant.
          expiry_tier: { bsonType: "string", enum: ["onchain","signer"] },

          state:    { bsonType: "string",
                      enum: ["pending","active","exhausted","expired","revoked"] },
          state_at: { bsonType: "date" },
          // SPL `approve` REPLACES the delegated amount rather than adding to it, so a
          // top-up starts a new (cap, drawn = 0) epoch. See ch. 08.
          epoch:    { bsonType: "int", minimum: 1 },

          open_key: { bsonType: "string" },   // materialised; see below

          // S4 · the velocity window lives on the same document as the budget, so ONE
          // atomic update can decide both rules. See ch. 04 and ch. 06.
          vel: { bsonType: "array", maxItems: 512, items: {
                   bsonType: "object", required: ["t","a"], additionalProperties: false,
                   properties: { t: { bsonType: "date" },
                                 a: { bsonType: "long", minimum: 1 } } } },
          created_at: { bsonType: "date" }
        } } },

    { $expr: { $and: [
        // I8 · a mainnet allowance cannot point at a sandbox agent.
        { $eq: [ { $arrayElemAt: [ { $split: ["$_id","_"] }, 1 ] },
                 { $arrayElemAt: [ { $split: ["$agent_id","_"] }, 1 ] } ] },
        { $eq: [ { $arrayElemAt: [ { $split: ["$_id","_"] }, 1 ] },
                 { $cond: [ { $eq: ["$network","sandbox"] }, "test", "live" ] } ] },

        // the deck's CHECK (drawn_onchain_base BETWEEN 0 AND cap_base) …
        { $lte: ["$drawn_onchain_base", "$cap_base"] },
        // … and the invariant it was standing in for.
        { $lte: [ { $add: ["$drawn_onchain_base","$reserved_base"] }, "$cap_base" ] },

        // the materialised open_key stays honest
        { $cond: [ { $in: ["$state", ["pending","active"]] },
                   { $eq: ["$open_key", { $concat: ["$agent_id","|","$mint"] }] },
                   { $eq: [ { $type: "$open_key" }, "missing" ] } ] },

        // I4 · `active` is a claim about the chain, so it needs a slot behind it.
        { $cond: [ { $eq: ["$state","active"] },
                   { $ne: [ { $type: "$last_read_slot" }, "missing" ] }, true ] }
    ]}}
  ]},
  validationLevel: "strict", validationAction: "error"
});

db.allowances.createIndex({ onchain_addr: 1 },
    { unique: true, name: "one_allowance_per_onchain_account" });
db.allowances.createIndex({ open_key: 1 },
    { unique: true, name: "one_active_allowance",
      partialFilterExpression: { open_key: { $exists: true } } });
db.allowances.createIndex({ agent_id: 1, state: 1 }, { name: "allowances_by_agent" });
db.allowances.createIndex({ network: 1, last_read_at: 1 }, { name: "allowances_due_for_refresh" });
```

**The fourth `$expr` clause is the most valuable line in the schema.** `drawn + reserved ≤ cap`
*is* the budget invariant, enforced by the database on every write. The signer's admission filter
cannot over-admit even if the filter is wrong, because the resulting document would be refused.
It is the direct analogue of a partial unique index making a concurrency rule structural instead
of hoped-for.

**Why `open_key` is materialised.** The literal translation of
`UNIQUE (agent_id, mint) WHERE state IN ('pending','active')` is a `partialFilterExpression` with
`$in`. That works on current servers, but the filter historically accepted only `$eq`,
`$exists:true`, the comparison operators, `$type` and `$and`. A schema whose correctness depends
on remembering which server version added which operator is a schema that will be wrong on
somebody's machine. `open_key` needs only `$exists`, which every version has, and the `$expr`
above makes the field and the state incapable of disagreeing. The transition out of
`pending`/`active` `$unset`s it in the same update that sets the state.

### `handshake_events`

```js
db.createCollection("handshake_events", {
  validator: { $and: [
    { $jsonSchema: {
        bsonType: "object",
        required: ["_id","payment_id","network","phase","at","writer"],
        additionalProperties: false,
        properties: {
          _id:        { bsonType: "string", pattern: "^hse_[0-9A-HJKMNP-TV-Z]{26}$" },
          payment_id: { bsonType: "string",
                        pattern: "^pay_(test|live)_[0-9A-HJKMNP-TV-Z]{26}$" },
          network:    { bsonType: "string", enum: ["sandbox","mainnet"] },
          phase:      { bsonType: "string",
                        enum: ["challenge_received","rules_evaluated","signed",
                               "replayed","broadcast","confirmed"] },
          // The signer writes 1–3, the indexer writes 5–6, and `confirmed` is the
          // indexer's alone (I4). Recorded, so a violation is visible in the data.
          writer:         { bsonType: "string", enum: ["signer","indexer"] },
          read_back_slot: { bsonType: ["long","null"] },
          detail:         { bsonType: ["object","null"] },
          detail_raw:     { bsonType: ["string","null"] },
          at:             { bsonType: "date" }
        } } },

    { $expr: { $and: [
        { $eq: [ { $arrayElemAt: [ { $split: ["$payment_id","_"] }, 1 ] },
                 { $cond: [ { $eq: ["$network","sandbox"] }, "test", "live" ] } ] },
        // I4 · `confirmed` only ever comes from the indexer, and only with a slot.
        { $cond: [ { $eq: ["$phase","confirmed"] },
                   { $and: [ { $eq: ["$writer","indexer"] },
                             { $ne: [ { $type: "$read_back_slot" }, "missing" ] },
                             { $ne: ["$read_back_slot", null] } ] },
                   true ] },
        { $cond: [ { $in: ["$phase",["challenge_received","rules_evaluated","signed"]] },
                   { $eq: ["$writer","signer"] }, true ] }
    ]}}
  ]},
  validationLevel: "strict", validationAction: "error"
});

db.handshake_events.createIndex({ payment_id: 1, phase: 1 },
    { unique: true, name: "one_phase_per_payment" });
db.handshake_events.createIndex({ payment_id: 1, at: 1 }, { name: "timeline_in_order" });
```

Note what that last `$expr` buys: **I4 is a schema constraint here, not a comment.** A code path
that writes `confirmed` from the signer, or without a slot, gets `DocumentFailedValidation`.

## Append-only, by privilege

The deck's `REVOKE UPDATE, DELETE ON sign_requests, handshake_events, policy_revisions` has a
MongoDB equivalent, with one important difference: **roles are purely additive. There is no
`REVOKE`.** You cannot grant `readWrite` and subtract from it. Every collection is enumerated,
every action is granted deliberately, and **the absence of a grant is the revocation**.

```js
db.getSiblingDB("admin").createRole({
  role: "leash_app",
  privileges: [
    // append-only. find + insert. No update. No remove. THIS is the REVOKE.
    { resource: { db:"leash", collection:"sign_requests"    }, actions:["find","insert"] },
    { resource: { db:"leash", collection:"handshake_events" }, actions:["find","insert","changeStream"] },
    { resource: { db:"leash", collection:"policy_revisions" }, actions:["find","insert"] },

    // payments: insert and update for the state machine, never remove.
    { resource: { db:"leash", collection:"payments" }, actions:["find","insert","update"] },

    { resource: { db:"leash", collection:"orgs"         }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"agents"       }, actions:["find","insert","update","changeStream"] },
    { resource: { db:"leash", collection:"allowances"   }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"policies"     }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"agent_keys"   }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"alerts"       }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"suppressions" }, actions:["find","insert","remove"] },
    { resource: { db:"leash", collection:"audit_jobs"   }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"templates"    }, actions:["find"] },
    { resource: { db:"leash", collection:"sign_claims"  }, actions:["find","insert","update"] },
    { resource: { db:"leash", collection:"siws_nonces"  }, actions:["find","insert","remove"] }
  ],
  roles: []
});
```

Two things to say plainly:

- **`bypassDocumentValidation` is never granted.** Without it, every validator above is
  unskippable for the application. With it, all of them evaporate at once. CI asserts the role
  document does not contain that action, and so does a boot-time check in production.
- **Migrations run as a different user**, `leash_migrator`, with `dbAdmin` and `readWrite`. It
  exists so the running application literally cannot `collMod` a validator away.

## Migrations

There is no `sqlx::migrate!`, and `golang-migrate`'s MongoDB driver takes raw JSON command files
— which for validators containing `$expr`, `$cond`, `$split` and `$arrayElemAt` would be
unreadable and unreviewable, defeating the entire point of pushing correctness into the database.
A migration also cannot reference `money.Max` or `network.Prefixes()` from a JSON file, so the
schema's constants would drift from the code's.

So: **numbered Go files and a small runner.**

```
0001_money_is_an_integer_number_of_micro_usdc.go
0002_an_owner_wallet_is_the_account.go
0003_an_agent_belongs_to_one_network_for_ever.go
0004_a_challenge_is_signed_exactly_once.go
0005_one_live_allowance_per_agent_and_mint.go
0006_a_policy_has_revisions_not_edits.go
0007_a_payment_leaves_six_phases_behind.go
0008_the_chain_is_read_at_a_slot.go
0009_an_alert_can_be_silenced_but_a_block_cannot.go
0010_an_export_is_a_job_not_a_response.go
0011_three_templates_a_new_owner_can_start_from.go
```

Each is `Migration{Version, Name, Up, Down}`, and the file name and the `Name` field must agree —
a test asserts it, because a migration whose name no longer describes it is worse than one with
no name at all.

**Applied at boot, before the port binds.** A service that starts serving and then discovers its
schema is wrong reports itself healthy while being useless, and a load balancer believes it.

**The lock** is a document in `schema_migrations` whose unique `_id` decides the race between two
instances of a rolling deploy — which is the normal case, not an edge case. A lock older than
five minutes is stealable, with a warning.

**The checksum** is a hash of the migration's rendered command set. A migration edited after it
was applied is detected at boot and refuses to start, so a quick fix to an old migration cannot
silently diverge development from production.

Reversibility is proved in CI, and proved harder than by asserting "down leaves nothing" —
see [13-testing.md](13-testing.md).

## The mapping, constraint by constraint

| PostgreSQL | MongoDB | Verdict |
|---|---|---|
| `orgs.owner_wallet UNIQUE` | unique index | exact |
| `agents.pubkey UNIQUE` | unique index | exact |
| `agents.idempotency_key UNIQUE` | unique index | exact |
| `allowances.onchain_addr UNIQUE` | unique index | exact |
| `sign_requests.challenge_hash UNIQUE` | unique index | exact |
| `payments.signature UNIQUE` | unique + `partialFilterExpression:{signature:{$exists:true}}` — PostgreSQL's UNIQUE already ignores nulls; MongoDB needs the partial to reproduce that | exact |
| `one_active_allowance … WHERE state IN (…)` | materialised `open_key` + `$exists` partial unique | exact, and version-proof |
| `one_payment_per_challenge` | unique index on `sign_request_id` | exact |
| `one_phase_per_payment` | compound unique index | exact |
| `CHECK (cap_base > 0)` and friends | `$jsonSchema` `{bsonType:"long", minimum:1}` | exact |
| `CHECK (drawn BETWEEN 0 AND cap_base)` | `$expr` — `$jsonSchema` cannot see a sibling field | exact |
| enums | `$jsonSchema` `enum` | exact |
| Trigger forbidding `UPDATE agents.network` | `_id` prefix + `$expr` | **stronger** |
| `REVOKE UPDATE, DELETE` | additive role granting `find`+`insert` | equivalent, with the caveat below |
| `REVOKE DELETE ON payments` | same | equivalent |
| Every foreign key | **nothing** | **not expressible** |
| `SELECT … FOR UPDATE` | conditional `findOneAndUpdate` | different, and better here |

## What is weaker

Five honest losses. They are repeated in [16-deck-conformance.md](16-deck-conformance.md) so they
cannot be forgotten during a rewrite.

**1 · `reserved_base` is a stored derived value.** In PostgreSQL the in-flight total is a live
`SUM()` inside a `SELECT … FOR UPDATE`, and it cannot drift because it is never stored. MongoDB
has no row lock, so the number is materialised — and a stored derived number can drift. Every
drift is either a refused payment that should have gone through or, worse, an admitted one that
should not. Three defences are in place: the validator's `drawn + reserved ≤ cap`; a recomputation
every 60 seconds that logs any delta as a warning; and a decrement that happens only in the same
atomic update that writes a fresh `drawn_onchain_base` and its slot, so every window is
**conservative** — money counted twice, never zero times. The risk is mitigated, not removed.
**This is the deviation to worry about first.**

**2 · There are no foreign keys.** `agents.org_id`, `payments.allowance_id`,
`handshake_events.payment_id` and the rest are unenforced. The identifier-prefix trick recovers
the *network* half of referential integrity — the half that carries I8 — but not existence.
Compensating control: `leashctl verify-referential-integrity` runs `$lookup` aggregations
asserting zero orphans, in CI and nightly against production. A cascade delete does not exist and
must never be written.

**3 · The append-only guarantee rests on one negative.** A single well-meaning
`grantRolesToUser(…, "readWrite")` silently converts the audit trail into an editable collection,
with no error anywhere. Mitigated by `verify-privileges` in CI and as a boot assertion. **Confirm
that the hosting tier permits custom database roles at all** — some managed shared tiers do not,
and on those, append-only degrades to application-enforced only, which is a materially weaker
claim to make to a customer and must be written down as such.

**4 · Cross-collection checks are impossible in general.** `payments.network == agents.network` is
expressible only because the network is part of the identifier. Anything needing a real join —
*"this payment's amount is at most its agent's `per_tx_max`"* — cannot be a constraint and lives
in the signer. `$expr` validators are also unindexed, cost CPU on every write, and are invisible
in code review unless the reviewer opens the migration. That is exactly what
`verify-constraints` exists to compensate for.

**5 · ULIDs are gapped.** `bigserial` gave a dense sequence, so an audit trail's completeness
could be proved by counting. ULIDs sort correctly by time but leave gaps. If a future compliance
conversation needs gapless numbering, it needs a counter document — one extra round trip and a
hot document — or an external notary. **Decide before that conversation, not during it.**

**6 · Two access layers reach this database.** `internal/store` in Go and `lib/store` in
TypeScript, where the discipline elsewhere is *all database access in one place*
([01-architecture.md](01-architecture.md)). Two implementations of the same shapes can disagree.

This is survivable for exactly one reason, and it is why the validators above are written the way
they are: **the enforcement point is the server, not either client.** A TypeScript route handler
and a Go job are constrained identically, neither can weaken a validator, and `verify-constraints`
proves the sixteen refusals against the server rather than against a client — so it proves them
for both at once.

What is *not* covered by the server is field naming and document shape drift, so ownership stays
single even though access does not: **each collection has exactly one writer**, per the table in
[01-architecture.md](01-architecture.md), and a checker asserts the TypeScript store has no method
that writes `agent_keys`, `sign_requests`, `sign_claims`, or an allowance's chain-read fields.

## Indexes, in one place

| Collection | Index | For |
|---|---|---|
| `orgs` | `{owner_wallet:1}` unique | sign-in |
| `agents` | `{pubkey:1}` unique · `{idempotency_key:1}` unique · `{org_id:1,network:1,created_at:-1}` | I7, the agents list |
| `allowances` | `{onchain_addr:1}` unique · `{open_key:1}` unique partial · `{agent_id:1,state:1}` · `{network:1,last_read_at:1}` | I7, `one_active_allowance`, refresh |
| `sign_requests` | `{challenge_hash:1}` unique · `{agent_id:1,_id:-1}` | I7, the FIFO reconstruction |
| `payments` | `{sign_request_id:1}` unique · `{signature:1}` unique partial · `{agent_id:1,state_at:-1}` · `{network:1,state:1,state_at:1}` · `{org_id:1,state_at:1}` | I7, the feed, the sweep, the export |
| `handshake_events` | `{payment_id:1,phase:1}` unique · `{payment_id:1,at:1}` | I7, the timeline |
| `policy_revisions` | `{agent_id:1,_id:-1}` | the revision history |
| `alerts` | `{org_id:1,sent_at:-1}` | the recent-alerts table |
| `audit_jobs` | `{org_id:1,_id:-1}` | the exports table |
| `siws_nonces` | `{expires_at:1}` TTL | expiry |
| `sign_claims` | `{state:1,created_at:1}` | the stale-claim sweeper |

`payments` carries denormalised `agent_name` and `host`. Both are immutable facts about that
payment, and the audit export is a `$lookup` rather than a SQL join — an unindexed join over
months of payments would be slow, and the 202-plus-job design exists to give room, not to excuse
it.
