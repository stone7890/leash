import "server-only";
import { db, toBig } from "./db";
import type {
  AllowanceState, Network, PaymentState, Phase, Tier, Verdict,
} from "@/lib/domain/state";

// Every read the dashboard needs. Plain objects out; no driver type escapes.
//
// EVERY network-scoped query takes the network as an argument. There is no overload that omits it,
// which is how "a query not filtered by network rejects the pull request" stops being a checklist
// item nobody remembers (I8).

export type Org = {
  id: string; ownerWallet: string; plan: string;
  activeNetwork: Network; workspaceName?: string;
};

export type Agent = {
  id: string; orgId: string; name: string; template: string;
  network: Network; runsAs?: string; pubkey: string;
  killed: boolean; createdAt: string;
};

export type Policy = {
  allowHosts: string[]; allowAll: boolean;
  perTxMax: bigint; velocityMax: bigint; velocityWindowS: number;
};

export type Allowance = {
  id: string; onchainAddr: string; mint: string;
  cap: bigint; drawn: bigint; reserved: bigint;
  state: AllowanceState;
  // I2: every number about the chain carries the slot it was read at. The interface shows it.
  lastReadSlot: number | null;
  expiryTs: string | null;
  // I3: the tier is DATA, because the adapter decides it. Never a constant in this app.
  expiryTier: Tier;
};

export type Payment = {
  id: string; agentId: string; agentName: string; host: string;
  payTo: string; amount: bigint; state: PaymentState;
  signature: string | null; confirmedSlot: number | null;
  stateAt: string; network: Network;
};

export type Check = { rule: string; name: string; tier: Tier; result: string; detail?: string };

export type SignRequest = {
  verdict: Verdict; failedRule?: string; checks: Check[];
};

export type TimelineEvent = {
  phase: Phase; writer: string; at: string;
  readBackSlot: number | null; detail?: Record<string, unknown>;
};

export async function orgForWallet(wallet: string): Promise<Org | null> {
  const d = await db();
  const o = await d.collection("orgs").findOne({ owner_wallet: wallet });
  if (!o) return null;
  return {
    id: o._id as unknown as string, ownerWallet: o.owner_wallet,
    plan: o.plan, activeNetwork: o.active_network, workspaceName: o.workspace_name,
  };
}

export async function listAgents(orgId: string, network: Network): Promise<Agent[]> {
  const d = await db();
  const rows = await d.collection("agents")
    .find({ org_id: orgId, network })
    .sort({ created_at: -1 }).limit(200).toArray();
  return rows.map(toAgent);
}

export async function getAgent(orgId: string, agentId: string): Promise<Agent | null> {
  const d = await db();
  // Scoped by organisation: a resource belonging to somebody else must be indistinguishable from
  // one that does not exist, or the identifier itself becomes an enumeration oracle.
  const a = await d.collection("agents").findOne({ _id: agentId as never, org_id: orgId });
  return a ? toAgent(a) : null;
}

function toAgent(a: Record<string, unknown>): Agent {
  return {
    id: a._id as string, orgId: a.org_id as string, name: a.name as string,
    template: a.template_id as string, network: a.network as Network,
    runsAs: a.runs_as as string | undefined, pubkey: a.pubkey as string,
    killed: Boolean(a.killed), createdAt: iso(a.created_at),
  };
}

export async function getPolicy(agentId: string): Promise<Policy | null> {
  const d = await db();
  const p = await d.collection("policies").findOne({ _id: agentId as never });
  if (!p) return null;
  return {
    allowHosts: (p.allow_hosts as string[]) ?? [],
    allowAll: Boolean(p.allow_all),
    perTxMax: toBig(p.per_tx_max_base),
    velocityMax: toBig(p.velocity_max_base),
    velocityWindowS: Number(p.velocity_window_s ?? 600),
  };
}

export async function liveAllowance(agentId: string): Promise<Allowance | null> {
  const d = await db();
  const a = await d.collection("allowances").findOne({
    agent_id: agentId, state: { $in: ["pending", "active"] },
  });
  if (!a) return null;
  return {
    id: a._id as unknown as string, onchainAddr: a.onchain_addr, mint: a.mint,
    cap: toBig(a.cap_base), drawn: toBig(a.drawn_onchain_base),
    reserved: toBig(a.reserved_base), state: a.state as AllowanceState,
    lastReadSlot: a.last_read_slot ? Number(a.last_read_slot) : null,
    expiryTs: a.expiry_ts ? iso(a.expiry_ts) : null,
    expiryTier: (a.expiry_tier as Tier) ?? "signer",
  };
}

export async function listPayments(
  orgId: string, network: Network, opts: { agentId?: string; limit?: number } = {},
): Promise<Payment[]> {
  const d = await db();
  const q: Record<string, unknown> = { org_id: orgId, network };
  if (opts.agentId) q.agent_id = opts.agentId;
  const rows = await d.collection("payments")
    .find(q).sort({ _id: -1 }).limit(opts.limit ?? 100).toArray();
  return rows.map((p) => ({
    id: p._id as unknown as string, agentId: p.agent_id, agentName: p.agent_name,
    host: p.host, payTo: p.pay_to, amount: toBig(p.amount_base),
    state: p.state as PaymentState, signature: p.signature ?? null,
    confirmedSlot: p.confirmed_slot ? Number(p.confirmed_slot) : null,
    stateAt: iso(p.state_at), network: p.network as Network,
  }));
}

export async function timeline(paymentId: string): Promise<TimelineEvent[]> {
  const d = await db();
  const rows = await d.collection("handshake_events")
    .find({ payment_id: paymentId }).sort({ at: 1 }).toArray();
  return rows.map((e) => ({
    phase: e.phase as Phase, writer: e.writer as string, at: iso(e.at),
    readBackSlot: e.read_back_slot ? Number(e.read_back_slot) : null,
    detail: e.detail as Record<string, unknown> | undefined,
  }));
}

/** The verdict behind a payment: which rules ran, and which one refused. */
export async function verdictFor(paymentId: string): Promise<SignRequest | null> {
  const d = await db();
  const p = await d.collection("payments").findOne({ _id: paymentId as never });
  if (!p) return null;
  const sr = await d.collection("sign_requests").findOne({ _id: p.sign_request_id as never });
  if (!sr) return null;
  return {
    verdict: sr.verdict as Verdict,
    failedRule: sr.failed_rule as string | undefined,
    checks: (sr.checks as Check[]) ?? [],
  };
}

/** Recent blocked verdicts, for the feed. A block produces no payment, so it is read from here. */
export async function recentBlocks(network: Network, limit = 20) {
  const d = await db();
  const rows = await d.collection("sign_requests")
    .find({ network, verdict: "blocked" }).sort({ _id: -1 }).limit(limit).toArray();
  return rows.map((r) => ({
    id: r._id as unknown as string, agentId: r.agent_id as string,
    failedRule: r.failed_rule as string | undefined,
    checks: (r.checks as Check[]) ?? [],
    createdAt: iso(r.created_at),
  }));
}

export async function chainCursor(network: Network) {
  const d = await db();
  const c = await d.collection("chain_cursors").findOne({ _id: network as never });
  if (!c) return null;
  return {
    slot: Number(c.last_slot ?? 0),
    lagSeconds: Number(c.lag_seconds ?? 0),
    using: (c.using as string) ?? "primary",
  };
}

/** The latest slot the dashboard can honestly show, from the allowances it has read back. */
export async function latestSlot(network: Network): Promise<number | null> {
  const d = await db();
  const a = await d.collection("allowances")
    .find({ network, last_read_slot: { $exists: true } })
    .sort({ last_read_slot: -1 }).limit(1).toArray();
  const slot = a[0]?.last_read_slot;
  return slot ? Number(slot) : null;
}

export async function listTemplates() {
  const d = await db();
  const rows = await d.collection("templates").find({}).sort({ sort_order: 1 }).toArray();
  return rows.map((t) => ({
    id: t._id as unknown as string, name: t.name as string,
    description: t.description as string,
    prefills: {
      cap: toBig((t.prefills as Record<string, unknown>).cap_base),
      perTxMax: toBig((t.prefills as Record<string, unknown>).per_tx_max_base),
      velocityMax: toBig((t.prefills as Record<string, unknown>).velocity_max_base),
      velocityWindowS: Number((t.prefills as Record<string, unknown>).velocity_window_s ?? 600),
      expiryDays: Number((t.prefills as Record<string, unknown>).expiry_days ?? 7),
      allowHosts: ((t.prefills as Record<string, unknown>).allow_hosts as string[]) ?? [],
    },
  }));
}

export async function listAlerts(orgId: string, limit = 20) {
  const d = await db();
  const rows = await d.collection("alerts")
    .find({ org_id: orgId }).sort({ created_at: -1 }).limit(limit).toArray();
  return rows.map((a) => ({
    id: a._id as unknown as string, kind: a.kind as string,
    body: (a.body as Record<string, unknown>) ?? {}, createdAt: iso(a.created_at),
  }));
}

function iso(v: unknown): string {
  if (v instanceof Date) return v.toISOString();
  if (typeof v === "string") return v;
  return new Date(0).toISOString();
}

// ── Activity: payments AND blocks, in one list ───────────────────────────────
//
// A blocked verdict produces NO payment — nothing was signed, so there is nothing to record as
// one. It lives in `sign_requests` instead. But an owner looking at their feed wants to see both,
// in order, because "the agent tried and was refused" is exactly as interesting as "the agent
// paid".
//
// They stay distinguishable in the type. Merging them into one shape would lose the thing that
// matters: a block has no amount that moved and no signature, and presenting it as a payment with
// those fields empty would suggest a payment that failed.

export type Activity =
  | { kind: "payment"; at: string; payment: Payment }
  | {
      kind: "block"; at: string; id: string; agentId: string; agentName: string;
      host: string; amount: bigint; failedRule: string; code: string; checks: Check[];
    };

export async function listActivity(
  orgId: string, network: Network, limit = 60,
): Promise<Activity[]> {
  const d = await db();

  const payments = await listPayments(orgId, network, { limit });

  // Blocks are scoped by the org's own agents: sign_requests carries no org, because the signer
  // does not need one to decide.
  const agents = await listAgents(orgId, network);
  const byId = new Map(agents.map((a) => [a.id, a]));
  const blocks = agents.length === 0 ? [] : await d.collection("sign_requests")
    .find({ network, verdict: "blocked", agent_id: { $in: agents.map((a) => a.id) } })
    .sort({ _id: -1 }).limit(limit).toArray();

  const out: Activity[] = payments.map((p) => ({
    kind: "payment" as const, at: p.stateAt, payment: p,
  }));

  for (const b of blocks) {
    const req = (b.requirements ?? {}) as Record<string, unknown>;
    const checks = (b.checks as Check[]) ?? [];
    out.push({
      kind: "block",
      at: iso(b.created_at),
      id: b._id as unknown as string,
      agentId: b.agent_id as string,
      agentName: byId.get(b.agent_id as string)?.name ?? "unknown agent",
      host: String(req.host ?? parseHost(b.requirements_raw as string | undefined) ?? "unknown"),
      amount: parseAmount(b.requirements_raw as string | undefined),
      failedRule: (b.failed_rule as string) ?? "",
      code: ruleCode(checks, b.failed_rule as string | undefined),
      checks,
    });
  }

  out.sort((a, b) => (a.at < b.at ? 1 : -1));
  return out.slice(0, limit);
}

/**
 * The amount, from the canonical form that is stored verbatim.
 *
 * The canonical form carries BASE UNITS — `amount=10000` is one cent, "exactly as the protocol
 * carries them" — so it is read as an integer, not parsed as a decimal. Parsing it as a decimal is
 * what made every blocked row in the activity table read $10000.00 for a one-cent refusal: a
 * million times over, on the screen whose whole job is to tell an owner how much was at stake.
 */
function parseAmount(canonical: string | undefined): bigint {
  const m = canonical ? /^amount=(-?\d+)$/m.exec(canonical) : null;
  if (!m?.[1]) return 0n;
  try { return BigInt(m[1]); } catch { return 0n; }
}

/**
 * The host cannot be recovered from the canonical form, and this says so.
 *
 * The canonical form EXCLUDES the host deliberately — the same challenge is the same payment
 * whichever name resolved to the server — which is exactly why the signer stores the host beside
 * it, in `requirements.host`. The fallback used to return the `pay_to=` line, so a sign request
 * written before that field existed would show a wallet address in the Endpoint column and offer
 * to add it to the allow list. Null renders as "unknown", which is the truth.
 */
function parseHost(_canonical: string | undefined): string | null {
  return null;
}

function ruleCode(checks: Check[], failed: string | undefined): string {
  const CODES: Record<string, string> = {
    S0: "NETWORK_MISMATCH", S1: "ALLOWANCE_INACTIVE", S2: "ENDPOINT_NOT_ALLOWED",
    S3: "PER_TX_LIMIT", S4: "VELOCITY_LIMIT", S5: "BUDGET_EXHAUSTED",
    S6: "UNSUPPORTED_TERMS", S7: "KILLED",
  };
  void checks;
  return failed ? (CODES[failed] ?? "") : "";
}

export async function getPayment(paymentId: string): Promise<Payment | null> {
  const d = await db();
  const p = await d.collection("payments").findOne({ _id: paymentId as never });
  if (!p) return null;
  return {
    id: p._id as unknown as string, agentId: p.agent_id, agentName: p.agent_name,
    host: p.host, payTo: p.pay_to, amount: toBig(p.amount_base),
    state: p.state as PaymentState, signature: p.signature ?? null,
    confirmedSlot: p.confirmed_slot ? Number(p.confirmed_slot) : null,
    stateAt: iso(p.state_at), network: p.network as Network,
  };
}
