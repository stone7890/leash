// The four independent axes. Merging any two is a data-model bug, and the interface keeps them
// apart too: `unknown` gets its own presentation, not a variant of failed.

export type Network = "sandbox" | "mainnet";
export type AllowanceState = "pending" | "active" | "exhausted" | "expired" | "revoked";
export type PaymentState = "signed" | "submitted" | "confirmed" | "failed" | "unknown";
export type Verdict = "allowed" | "blocked";
export type Tier = "onchain" | "signer";

export type Phase =
  | "challenge_received" | "rules_evaluated" | "signed"
  | "replayed" | "broadcast" | "confirmed";

export const PHASE_ORDER: Phase[] = [
  "challenge_received", "rules_evaluated", "signed", "replayed", "broadcast", "confirmed",
];

export const PHASE_LABEL: Record<Phase, string> = {
  challenge_received: "Challenge received",
  rules_evaluated: "Rules evaluated",
  signed: "Signed within allowance",
  replayed: "Replayed with X-PAYMENT",
  broadcast: "Broadcast by endpoint",
  confirmed: "Confirmed on Solana — read back",
};

/** Payments still holding budget. `unknown` is one of them: I4. */
export function inFlight(s: PaymentState): boolean {
  return s === "signed" || s === "submitted" || s === "unknown";
}

/**
 * Colour is never the only carrier, so every state also has its word. The pill renders both.
 */
export const PAYMENT_TONE: Record<PaymentState, "ok" | "wait" | "bad"> = {
  confirmed: "ok", signed: "wait", submitted: "wait", unknown: "wait", failed: "bad",
};

export const ALLOWANCE_TONE: Record<AllowanceState, "ok" | "wait" | "bad"> = {
  active: "ok", pending: "wait", exhausted: "wait", expired: "bad", revoked: "bad",
};

/** Which network a Leash identifier belongs to, readable straight from the string. */
export function networkOf(id: string): Network | null {
  if (id.includes("_test_")) return "sandbox";
  if (id.includes("_live_")) return "mainnet";
  return null;
}
