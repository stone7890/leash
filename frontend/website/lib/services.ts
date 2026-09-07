import "server-only";

// Talking to the Go services.
//
// The dashboard cannot mint an agent key — nothing here may import a KMS client (I1) — and it does
// not build Solana transactions, because there must be exactly one implementation of what a
// delegation looks like and it lives in the adapter (docs/08-onchain-adapter.md).
//
// So these two calls exist. Both are internal: nginx exposes /v1/sign of the signer and the SSE
// stream of the indexer, and nothing else, and both additionally require a shared token.

function internalToken(): string {
  const v = process.env.INTERNAL_TOKEN;
  if (!v) throw new Error("INTERNAL_TOKEN is not set");
  return v;
}

function signerBase(): string {
  return process.env.SIGNER_BASE_URL || "http://signer:4100";
}

function indexerBase(): string {
  // Read PER REQUEST, not baked in at build time: the target differs between deployments, and a
  // value compiled into the bundle would need a rebuild to change.
  return process.env.INDEXER_BASE_URL || "http://indexer:4300";
}

async function post<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-leash-internal": internalToken(),
    },
    body: JSON.stringify(body),
    cache: "no-store",
  });
  const text = await res.text();
  if (!res.ok) {
    // The service's own envelope is passed through where there is one, so a rule name or a chain
    // error reaches the interface rather than being flattened into "something went wrong".
    try {
      const parsed = JSON.parse(text) as { error?: { message?: string; code?: string } };
      throw new ServiceError(
        parsed.error?.code ?? "INTERNAL",
        parsed.error?.message ?? text,
        res.status,
      );
    } catch (e) {
      if (e instanceof ServiceError) throw e;
      throw new ServiceError("INTERNAL", text || res.statusText, res.status);
    }
  }
  return JSON.parse(text) as T;
}

export class ServiceError extends Error {
  constructor(readonly code: string, message: string, readonly status: number) {
    super(message);
    this.name = "ServiceError";
  }
}

/** Mint an agent's keypair. Only the signer can do this; it holds every key. */
export function provisionAgentKey(agentId: string, network: string) {
  return post<{ pubkey: string; api_key: string }>(
    `${signerBase()}/internal/agents/keys`,
    { agent_id: agentId, network },
  );
}

export function rotateAgentKey(agentId: string) {
  return post<{ pubkey: string; api_key: string }>(
    `${signerBase()}/internal/agents/keys/rotate`,
    { agent_id: agentId },
  );
}

export type Intent = {
  transaction: string;
  blockhash: string;
  expires_at: string;
  allowance_account: string;
  summary: {
    cap: string;
    expiry_ts: string;
    delegate_to: string;
    network_fee: string;
    rent_sol: string;
    funds_move: boolean;
    expiry_tier: "onchain" | "signer";
  };
};

/** Build an unsigned transaction for the owner's wallet. Leash never signs one. */
export function buildIntent(body: {
  kind: "delegation" | "modify" | "revoke";
  owner: string;
  agent?: string;
  network: string;
  cap?: string;
  expiry_days?: number;
  allowance_account?: string;
  sweep_to?: string;
}) {
  return post<Intent>(`${indexerBase()}/internal/intents`, body);
}

/** Broadcast a transaction the owner has already signed in their wallet. */
export function submitSigned(transaction: string, network: string) {
  return post<{ signature: string }>(`${indexerBase()}/internal/submit`, { transaction, network });
}

/** Run the real P2 loop for onboarding step 5. */
export function startTestPayment(apiKey: string, network: string) {
  return post<{ payment_id: string }>(
    `${indexerBase()}/internal/test-payments`,
    { api_key: apiKey, network },
  );
}
