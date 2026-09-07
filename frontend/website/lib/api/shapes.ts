import type {
  Agent, Allowance, Payment, Policy, SignRequest, TimelineEvent,
} from "@/lib/store/queries";

/**
 * The wire shapes, and the only place the API's field names are decided.
 *
 * The contract says field names are `snake_case` ([docs/05-api-contract.md]), and the store layer
 * speaks the app's own camelCase types. Handing a query result straight to `json()` published the
 * second as if it were the first — `perTxMax` where the documented example says `per_tx_max`,
 * `readBackSlot` where the timeline is documented as `read_back_slot`. That was not merely untidy:
 * `handshake-log`'s polling fallback reads `read_back_slot`, so a payment whose SSE stream dropped
 * lost the slot it was confirmed at, which is the one number invariant I2 insists travels with a
 * chain reading.
 *
 * Money stays a `bigint` here. `json()` renders it as a six-place string, which is the other half
 * of the contract — a JSON number would be an IEEE double (I6).
 */

export function agentResponse(a: Agent) {
  return {
    id: a.id,
    org_id: a.orgId,
    name: a.name,
    template: a.template,
    network: a.network,
    runs_as: a.runsAs,
    pubkey: a.pubkey,
    killed: a.killed,
    created_at: a.createdAt,
  };
}

export function policyResponse(p: Policy | null) {
  if (!p) return null;
  return {
    allow_hosts: p.allowHosts,
    allow_all: p.allowAll,
    per_tx_max: p.perTxMax,
    velocity_max: p.velocityMax,
    velocity_window_s: p.velocityWindowS,
  };
}

export function allowanceResponse(a: Allowance | null) {
  if (!a) return null;
  return {
    id: a.id,
    onchain_addr: a.onchainAddr,
    mint: a.mint,
    cap: a.cap,
    drawn: a.drawn,
    reserved: a.reserved,
    state: a.state,
    // I2 and I3 travel as data, under the names the contract publishes them by.
    last_read_slot: a.lastReadSlot,
    expiry_ts: a.expiryTs,
    expiry_tier: a.expiryTier,
  };
}

export function paymentResponse(p: Payment) {
  return {
    id: p.id,
    agent_id: p.agentId,
    agent_name: p.agentName,
    host: p.host,
    pay_to: p.payTo,
    amount: p.amount,
    state: p.state,
    signature: p.signature,
    confirmed_slot: p.confirmedSlot,
    state_at: p.stateAt,
    network: p.network,
  };
}

export function timelineEventResponse(e: TimelineEvent) {
  return {
    phase: e.phase,
    writer: e.writer,
    at: e.at,
    read_back_slot: e.readBackSlot,
    detail: e.detail,
  };
}

export function signRequestResponse(s: SignRequest | null) {
  if (!s) return null;
  return {
    verdict: s.verdict,
    failed_rule: s.failedRule,
    checks: s.checks,
  };
}

/**
 * A template's prefills, under the names the contract publishes.
 *
 * The documented example is snake_case down to `velocity_window_s`; the query layer answers in the
 * app's types. Nothing in this app calls this endpoint — both screens read the store directly —
 * which is exactly why it drifted: the only caller who would have noticed is somebody else's.
 */
export function templateResponse(t: {
  id: string; name: string; description: string;
  prefills: {
    cap: bigint; perTxMax: bigint; velocityMax: bigint;
    velocityWindowS: number; expiryDays: number; allowHosts: string[];
  };
}) {
  return {
    id: t.id,
    name: t.name,
    description: t.description,
    prefills: {
      cap: t.prefills.cap,
      per_tx_max: t.prefills.perTxMax,
      velocity_max: t.prefills.velocityMax,
      velocity_window_s: t.prefills.velocityWindowS,
      expiry_days: t.prefills.expiryDays,
      allow_hosts: t.prefills.allowHosts,
    },
  };
}

export function alertResponse(a: {
  id: string; kind: string; body: Record<string, unknown>; createdAt: string;
}) {
  return { id: a.id, kind: a.kind, body: a.body, created_at: a.createdAt };
}
