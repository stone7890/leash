import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * The sandbox faucet.
 *
 * A brand-new owner has no sandbox USDC and no SOL, so without this the wizard stops at step 3
 * with a transaction that cannot pay its own fee. The specification's step N-01 grants 100 test
 * USDC, and this is where that happens.
 *
 * Rate limited per wallet: the sandbox has no other limits by design — it is play money and the
 * point is that a developer can spend it freely — but the faucet is the one part of it that costs
 * us something.
 */
export async function POST(req: NextRequest) {
  try {
    const caller = await requireOwner();

    if (!req.headers.get("idempotency-key")?.trim()) {
      return fail("MISSING_IDEMPOTENCY_KEY", "an Idempotency-Key header is required", 400);
    }

    const allowed = await claimGrant(caller.wallet);
    if (!allowed) {
      return fail("RATE_LIMITED", "the faucet has already funded this wallet recently", 429,
        { retriable: true });
    }

    const res = await fetch(`${indexerBase()}/internal/faucet`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-leash-internal": internalToken(),
      },
      body: JSON.stringify({ wallet: caller.wallet, network: "sandbox" }),
      cache: "no-store",
    });
    const body = await res.json().catch(() => null);
    if (!res.ok) {
      const message = body?.error?.message ?? "the faucet could not fund this wallet";
      return fail(body?.error?.code ?? "CHAIN_UNREACHABLE", message, 503, { retriable: true });
    }
    // 202: the transfers are on the chain. The wallet's balance is read back, not asserted here.
    return json(body, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    if (e instanceof ServiceError) return fail(e.code, e.message, 503, { retriable: true });
    throw e;
  }
}

function internalToken(): string {
  const v = process.env.INTERNAL_TOKEN;
  if (!v) throw new Error("INTERNAL_TOKEN is not set");
  return v;
}

function indexerBase(): string {
  return process.env.INDEXER_BASE_URL || "http://indexer:4300";
}

/**
 * One grant per wallet per hour.
 *
 * Recorded in the database, not in memory: an in-memory counter does not survive a restart and is
 * not shared between instances, and both failures are silent — the limit simply stops applying.
 *
 * The row is claimed BEFORE the grant is made. Claiming afterwards would let two simultaneous
 * requests both check, both find nothing, and both mint.
 */
async function claimGrant(wallet: string): Promise<boolean> {
  const { db } = await import("@/lib/store/db");
  const { newId } = await import("@/lib/store/writes");
  const d = await db();

  const perHour = Number(process.env.FAUCET_PER_WALLET_PER_HOUR ?? "1");
  const now = new Date();
  const since = new Date(now.getTime() - 3600_000);

  const used = await d.collection("faucet_grants").countDocuments({
    wallet, granted_at: { $gte: since },
  });
  if (used >= perHour) return false;

  await d.collection("faucet_grants").insertOne({
    _id: newId("job") as never,
    wallet,
    network: "sandbox",
    granted_at: now,
    // The rows are a rate limit, not a record worth keeping. They expire on their own.
    expires_at: new Date(now.getTime() + 3600_000),
  });
  return true;
}
