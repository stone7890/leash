import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, getPolicy } from "@/lib/store/queries";
import { addAllowedHost } from "@/lib/store/writes";

export const dynamic = "force-dynamic";

/**
 * The one-tap recovery button, from flow P3.
 *
 * It adds EXACTLY ONE host — the one from the challenge that was blocked. The rule is enforced
 * here as well as in the interface, because the button is not the only caller, and the trap the
 * specification names for this flow is a recovery action that quietly widens an allow-list beyond
 * the host that was actually refused.
 *
 * A wildcard, a list, or a parent domain is a 422. Not a silent narrowing — a refusal, so the
 * caller learns the rule rather than getting something other than what it asked for.
 */
export async function PATCH(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const body = (await req.json().catch(() => null)) as { host?: string } | null;
    const host = body?.host?.trim().toLowerCase();
    if (!host) {
      return fail("MALFORMED_REQUEST", "a host is required", 400, { field: "host" });
    }

    if (host === "*" || host.includes("*")) {
      return fail("TOO_MANY_HOSTS",
        "a wildcard cannot be added here — it would allow every endpoint, not the one that was " +
        "blocked", 422, { field: "host" });
    }
    if (host.includes(",") || host.includes(" ")) {
      return fail("TOO_MANY_HOSTS", "exactly one host may be added", 422, { field: "host" });
    }
    if (!/^[a-z0-9.-]+(:\d+)?$/.test(host)) {
      return fail("INVALID_HOST", "not a hostname", 422, { field: "host" });
    }

    // A parent of something already allowed would widen every entry beneath it at once.
    const policy = await getPolicy(agent.id);
    const widens = (policy?.allowHosts ?? []).some((h) => h !== host && h.endsWith("." + host));
    if (widens) {
      return fail("TOO_MANY_HOSTS",
        `${host} is a parent of a host this agent already allows, so adding it would widen more ` +
        "than the endpoint that was blocked", 422, { field: "host" });
    }

    await addAllowedHost(agent.id, host, caller.wallet);

    // Never retroactive. The interface says so too: "In effect from the next payment."
    return json({
      host,
      effective: "from the next payment",
      allow_hosts: (await getPolicy(agent.id))?.allowHosts ?? [],
    });
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
