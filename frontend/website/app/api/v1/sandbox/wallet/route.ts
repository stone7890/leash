import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * What the signed-in owner's wallet holds on the sandbox.
 *
 * The wizard's third step needs this before it opens a wallet: the delegation moves the cap out of
 * the owner's token account, so a wallet without it meets a transaction that fails for a reason it
 * cannot see. The screen used to answer that question with "did the faucet just work in this
 * browser session", which is only the same question for the first agent of the hour on a page that
 * has not been reloaded — every other time the faucet's own rate limit locked the owner out of a
 * delegation they could well afford.
 *
 * Not rate limited: reading a balance costs nothing and refusing the read is the failure mode this
 * exists to remove.
 */
export async function GET() {
  try {
    const caller = await requireOwner();

    const url = `${indexerBase()}/internal/wallet` +
      `?wallet=${encodeURIComponent(caller.wallet)}&network=sandbox`;
    const res = await fetch(url, {
      headers: { "x-leash-internal": internalToken() },
      cache: "no-store",
    });
    const body = await res.json().catch(() => null);
    if (!res.ok) {
      return fail(body?.error?.code ?? "CHAIN_UNREACHABLE",
        body?.error?.message ?? "could not read this wallet", 503, { retriable: true });
    }
    return json(body, 200);
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
