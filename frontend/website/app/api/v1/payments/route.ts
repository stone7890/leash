import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { listPayments } from "@/lib/store/queries";
import type { Network } from "@/lib/domain/state";

export const dynamic = "force-dynamic";

export async function GET(req: NextRequest) {
  try {
    const caller = await requireOwner();
    const raw = req.nextUrl.searchParams.get("network") ?? "sandbox";
    if (raw !== "sandbox" && raw !== "mainnet") {
      return fail("INVALID_NETWORK", "network must be sandbox or mainnet", 422);
    }
    const agentId = req.nextUrl.searchParams.get("agent") ?? undefined;
    return json(await listPayments(caller.org, raw as Network, { agentId, limit: 100 }));
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
