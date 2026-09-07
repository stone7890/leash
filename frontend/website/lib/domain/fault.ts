// The error envelope, and the copy that goes with each code.
//
// The codes are frozen and come from contracts/error-codes.json — the same file the Go runtime is
// checked against, so the two cannot drift. `message` from the server is for a person and may be
// reworded; the interface keys its own copy off `code`.

export type ErrorBody = {
  code: string;
  message: string;
  field?: string;
  details?: unknown;
  retriable: boolean;
  trace_id?: string;
};

export type Envelope = { error: ErrorBody };

export class ApiError extends Error {
  constructor(readonly status: number, readonly body: ErrorBody) {
    super(body.message);
    this.name = "ApiError";
  }
  get code() { return this.body.code; }
  get retriable() { return this.body.retriable; }
}

/**
 * The copy for a blocked payment.
 *
 * Fixed wording, from docs/10-ui-spec.md. Each of these tells a user something true that they
 * would otherwise assume wrongly — above all that a block moved no money.
 */
export const RULE_COPY: Record<string, { name: string; blocked: string }> = {
  NETWORK_MISMATCH:    { name: "network",     blocked: "the key and the challenge are on different networks" },
  ALLOWANCE_INACTIVE:  { name: "allowance",   blocked: "this agent's allowance is not active" },
  ENDPOINT_NOT_ALLOWED:{ name: "allow-list",  blocked: "not on the allow list" },
  PER_TX_LIMIT:        { name: "per-payment", blocked: "over the per-payment maximum" },
  VELOCITY_LIMIT:      { name: "velocity",    blocked: "over the ten-minute limit" },
  BUDGET_EXHAUSTED:    { name: "budget",      blocked: "the remaining budget is too small" },
  UNSUPPORTED_TERMS:   { name: "mint",        blocked: "the payment terms are not supported" },
  KILLED:              { name: "kill switch", blocked: "this agent has been stopped" },
};

/** Copy that must be used verbatim. */
export const COPY = {
  unknownPayment:
    "This payment timed out before we saw a result. It is neither failed nor confirmed — we keep " +
    "checking the chain, and it will never be signed twice.",
  walletRejected:
    "The wallet rejected the transaction. Nothing was created and nothing moved.",
  switchingToMainnet:
    "Mainnet. Delegations here move real USDC — caps still protect you.",
  nothingMoved: "nothing was signed, nothing moved",
  connectingReadsOnly:
    "Connecting only reads your address. Nothing moves without a transaction you sign.",
  sandboxFirst:
    "First time? You'll start in the sandbox — a test network with play-money USDC. Nothing real " +
    "until you switch.",
  nonCustodial:
    "Non-custodial. Your funds stay in your wallet; Leash holds keys to nothing.",
} as const;
