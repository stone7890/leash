import { templateResponse } from "@/lib/api/shapes";
import { requireOwner, unauthenticated, json, Unauthenticated } from "@/lib/auth/require";
import { listTemplates } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

export async function GET() {
  try {
    await requireOwner(); // the first statement, always
    return json((await listTemplates()).map(templateResponse));
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
