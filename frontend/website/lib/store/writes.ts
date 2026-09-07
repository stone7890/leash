import "server-only";
import type { ClientSession } from "mongodb";
import { client, db, toLong } from "./db";
import type { Network, Tier } from "@/lib/domain/state";

// The writes the dashboard performs. Reads live in queries.ts.
//
// Every one of these is network-scoped and takes the network explicitly. There is no variant that
// infers it, because a default network is the one thing capable of creating a mainnet record
// during a sandbox operation.

/** A ULID with a kind prefix, matching what the Go side mints. */
export function newId(kind: string, network?: Network): string {
  const A = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let t = Date.now();
  let out = "";
  for (let i = 9; i >= 0; i--) { out = A[t % 32] + out; t = Math.floor(t / 32); }
  for (let i = 0; i < 16; i++) out += A[Math.floor(Math.random() * 32)];
  // The network is part of the identifier, which is what makes it immutable: MongoDB refuses any
  // update that modifies _id, so an agent cannot change networks (I8).
  const net = network ? (network === "sandbox" ? "test_" : "live_") : "";
  return `${kind}_${net}${out}`;
}

export type NewAgentInput = {
  orgId: string;
  name: string;
  template: string;
  network: Network;
  runsAs: string;
  idempotencyKey: string;
  policy: {
    allowHosts: string[];
    allowAll: boolean;
    perTxMax: bigint;
    velocityMax: bigint;
    velocityWindowS: number;
  };
};

/**
 * Create the agent and its policy in ONE transaction.
 *
 * The key is provisioned separately, by the signer, and written back with `attachPubkey`. An agent
 * without a key is one the signer would refuse in a way nobody could diagnose, so the caller
 * removes it if provisioning fails.
 */
export async function createAgent(input: NewAgentInput): Promise<string> {
  const d = await db();
  const id = newId("agt", input.network);
  const now = new Date();

  // One transaction. An agent with no policy is an agent the signer would refuse for a reason
  // nobody could work out, and the three documents belong together or not at all.
  const session = (await client()).startSession();
  try {
    await session.withTransaction(async () => {
      await insertAll(d, id, input, now, session);
    });
  } finally {
    await session.endSession();
  }
  return id;
}

async function insertAll(
  d: Awaited<ReturnType<typeof db>>,
  id: string,
  input: NewAgentInput,
  now: Date,
  session: ClientSession,
) {
  await d.collection("agents").insertOne({
    _id: id as never,
    org_id: input.orgId,
    name: input.name,
    template_id: input.template,
    network: input.network,
    runs_as: input.runsAs,
    // Filled in by attachPubkey once the signer has minted the key. The schema requires it, so a
    // placeholder that could never be a real address is used and immediately replaced.
    pubkey: "11111111111111111111111111111111",
    idempotency_key: input.idempotencyKey,
    killed: false,
    created_at: now,
  }, { session });

  await d.collection("policies").insertOne({
    _id: id as never,
    allow_hosts: input.policy.allowHosts,
    allow_all: input.policy.allowAll,
    per_tx_max_base: toLong(input.policy.perTxMax),
    velocity_max_base: toLong(input.policy.velocityMax),
    velocity_window_s: input.policy.velocityWindowS,
    updated_at: now,
  }, { session });

  await d.collection("policy_revisions").insertOne({
    _id: newId("rev") as never,
    agent_id: id,
    change: { created: input.template },
    actor: "owner",
    at: now,
  }, { session });
}

export async function attachPubkey(agentId: string, pubkey: string) {
  const d = await db();
  await d.collection("agents").updateOne({ _id: agentId as never }, { $set: { pubkey } });
}

/** Remove an agent that never got a key. It has no history, so there is nothing to preserve. */
export async function removeIncompleteAgent(agentId: string) {
  const d = await db();
  const signed = await d.collection("sign_requests").countDocuments({ agent_id: agentId });
  if (signed > 0) return; // it has history; leave it alone
  await d.collection("policies").deleteOne({ _id: agentId as never });
  await d.collection("policy_revisions").deleteMany({ agent_id: agentId });
  await d.collection("agents").deleteOne({ _id: agentId as never });
}

/**
 * Record the allowance as PENDING.
 *
 * It becomes active only through a read-back — the schema refuses an active allowance with no slot
 * — so there is no path here that could mark it live (I4).
 */
export async function createPendingAllowance(input: {
  agentId: string;
  network: Network;
  onchainAddr: string;
  mint: string;
  tokenProgram: string;
  cap: bigint;
  expiryDays: number;
  expiryTier: Tier;
}): Promise<string> {
  const d = await db();
  const id = newId("alw", input.network);
  const now = new Date();
  await d.collection("allowances").insertOne({
    _id: id as never,
    agent_id: input.agentId,
    network: input.network,
    onchain_addr: input.onchainAddr,
    mint: input.mint,
    token_program_id: input.tokenProgram,
    cap_base: toLong(input.cap),
    drawn_onchain_base: toLong(0n),
    reserved_base: toLong(0n),
    expiry_ts: new Date(now.getTime() + input.expiryDays * 86400_000),
    // I3: the tier comes from the adapter, through the intent's summary. Never a constant here.
    expiry_tier: input.expiryTier,
    state: "pending",
    state_at: now,
    epoch: 1,
    // Present exactly while the allowance can still spend; the unique partial index over it is
    // what stops one agent holding two live budgets for one mint.
    open_key: `${input.agentId}|${input.mint}`,
    vel: [],
    created_at: now,
  });
  return id;
}

/** Has this Idempotency-Key been used? A retry must return the original agent, not a second one. */
export async function agentForIdempotencyKey(key: string): Promise<string | null> {
  const d = await db();
  const a = await d.collection("agents").findOne({ idempotency_key: key });
  return a ? (a._id as unknown as string) : null;
}

/**
 * Add one allowed host, and record why, in the same transaction.
 *
 * The revision and the change land together or not at all: an audit trail with gaps at exactly the
 * interesting moments is worse than none, because it is trusted.
 *
 * `$addToSet`, so pressing the recovery button twice is not an error and does not duplicate.
 */
export async function addAllowedHost(agentId: string, host: string, actor: string) {
  const d = await db();
  const now = new Date();
  const session = (await client()).startSession();
  try {
    await session.withTransaction(async () => {
      await d.collection("policies").updateOne(
        { _id: agentId as never },
        { $addToSet: { allow_hosts: host }, $set: { updated_at: now } },
        { session },
      );
      await d.collection("policy_revisions").insertOne({
        _id: newId("rev") as never,
        agent_id: agentId,
        change: { allow_host_added: host },
        actor,
        at: now,
      }, { session });
    });
  } finally {
    await session.endSession();
  }
}

/**
 * Silence the alert for one (agent, host) pair.
 *
 * The composite document _id reproduces the specification's PRIMARY KEY (agent_id, host) exactly:
 * uniqueness, no second index, and a free upsert by key. Field order inside a document _id is
 * significant to MongoDB's equality, so it is constructed here, in one place, and never from a map
 * whose iteration order is not guaranteed.
 */
export async function suppressAlert(agentId: string, host: string) {
  const d = await db();
  await d.collection("suppressions").updateOne(
    { _id: { a: agentId, h: host } as never },
    { $setOnInsert: { created_at: new Date() } },
    { upsert: true },
  );
}

/**
 * Set the kill flag.
 *
 * This is the soft tier of the kill switch, and it takes effect at once: the signer watches a
 * change stream on this field and drops the agent's cached snapshot within milliseconds. A
 * 60-second cache TTL would be far too slow for a control whose whole promise is "instantly".
 */
export async function killAgent(agentId: string) {
  const d = await db();
  await d.collection("agents").updateOne(
    { _id: agentId as never },
    { $set: { killed: true, killed_at: new Date() } },
  );
}
