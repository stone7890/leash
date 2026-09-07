import { requireOwner, unauthenticated, json, Unauthenticated } from "@/lib/auth/require";
import { listAlerts } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

export async function GET() {
  try {
    const caller = await requireOwner();
    return json(await listAlerts(caller.org));
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
