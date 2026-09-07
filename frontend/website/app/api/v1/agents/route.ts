import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { listAgents, liveAllowance, getPolicy, getAgent, listTemplates } from "@/lib/store/queries";
import {
  agentForIdempotencyKey, attachPubkey, createAgent, removeIncompleteAgent,
} from "@/lib/store/writes";
import { provisionAgentKey, ServiceError } from "@/lib/services";
import { parseMoney } from "@/lib/domain/money";
import type { Network } from "@/lib/domain/state";

export const dynamic = "force-dynamic";

export async function GET(req: NextRequest) {
  try {
    const caller = await requireOwner();

    // The network is a REQUIRED parameter, not a default. There is no listing that spans both.
    const raw = req.nextUrl.searchParams.get("network") ?? "sandbox";
    if (raw !== "sandbox" && raw !== "mainnet") {
      return fail("INVALID_NETWORK", "network must be sandbox or mainnet", 422,
        { field: "network" });
    }
    const network = raw as Network;

    const agents = await listAgents(caller.org, network);
    const withState = await Promise.all(agents.map(async (a) => ({
      ...a,
      policy: await getPolicy(a.id),
      allowance: await liveAllowance(a.id),
    })));
    return json(withState);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}

/**
 * Create an agent.
 *
 * The keypair is minted by the SIGNER, not here: nothing under frontend/website may import a KMS
 * client, and the signer is the only holder of keys. So this writes the agent and its policy, asks
 * the signer for a key, and removes the agent if that fails — an agent without a key is one the
 * signer would refuse for a reason nobody could diagnose.
 *
 * The API key comes back ONCE, in this response, and is never recoverable afterwards.
 */
export async function POST(req: NextRequest) {
  try {
    const caller = await requireOwner();

    const idempotencyKey = req.headers.get("idempotency-key")?.trim();
    if (!idempotencyKey) {
      return fail("MISSING_IDEMPOTENCY_KEY", "an Idempotency-Key header is required", 400);
    }

    const body = (await req.json().catch(() => null)) as {
      name?: string; template_id?: string; network?: string; runs_as?: string;
      cap?: string; per_tx_max?: string; velocity_max?: string;
      allow_hosts?: string[]; allow_all?: boolean; expiry_days?: number;
    } | null;
    if (!body?.name?.trim()) {
      return fail("MALFORMED_REQUEST", "a name is required", 400, { field: "name" });
    }
    const network = (body.network ?? "sandbox") as Network;
    if (network !== "sandbox" && network !== "mainnet") {
      return fail("INVALID_NETWORK", "network must be sandbox or mainnet", 422,
        { field: "network" });
    }

    // A retry returns the ORIGINAL agent rather than making a second one. Enforced by a unique
    // index as well, so two instances racing cannot both create.
    const existing = await agentForIdempotencyKey(idempotencyKey);
    if (existing) {
      const agent = await getAgent(caller.org, existing);
      if (!agent) return fail("IDEMPOTENCY_CONFLICT", "that key was used by another workspace", 409);
      // The API key is NOT replayed: it was shown once, and showing it again would make "once"
      // untrue for anybody who saw the first response.
      return json({ ...agent, api_key: null, replayed: true });
    }

    const templates = await listTemplates();
    const template = templates.find((t) => t.id === (body.template_id ?? "blank"))
      ?? templates.find((t) => t.id === "blank");
    if (!template) return fail("INTERNAL", "no templates are seeded", 500);

    let cap: bigint, perTxMax: bigint, velocityMax: bigint;
    try {
      cap = body.cap ? parseMoney(body.cap) : template.prefills.cap;
      perTxMax = body.per_tx_max ? parseMoney(body.per_tx_max) : template.prefills.perTxMax;
      velocityMax = body.velocity_max ? parseMoney(body.velocity_max) : template.prefills.velocityMax;
    } catch (e) {
      return fail("INVALID_AMOUNT", e instanceof Error ? e.message : "invalid amount", 422,
        { field: "cap" });
    }

    const allowHosts = (body.allow_hosts ?? template.prefills.allowHosts)
      .map((h) => h.trim().toLowerCase())
      .filter((h) => h.length > 0 && h !== "*");
    const allowAll = Boolean(body.allow_all) || (body.allow_hosts ?? []).includes("*");

    const agentId = await createAgent({
      orgId: caller.org,
      name: body.name.trim(),
      template: template.id,
      network,
      runsAs: body.runs_as ?? "cli",
      idempotencyKey,
      policy: {
        allowHosts, allowAll, perTxMax, velocityMax,
        velocityWindowS: template.prefills.velocityWindowS,
      },
    });

    let key: { pubkey: string; api_key: string };
    try {
      key = await provisionAgentKey(agentId, network);
    } catch (e) {
      // Leave nothing half-made behind.
      await removeIncompleteAgent(agentId);
      const message = e instanceof ServiceError ? e.message : "could not create the agent key";
      return fail("INTERNAL", message, 502, { retriable: true });
    }
    await attachPubkey(agentId, key.pubkey);

    const agent = await getAgent(caller.org, agentId);
    return json({
      ...agent,
      // Once. The database stores a hash, so it cannot be shown again even by us.
      api_key: key.api_key,
      cap: cap,
      expiry_days: body.expiry_days ?? template.prefills.expiryDays,
    }, 201);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
