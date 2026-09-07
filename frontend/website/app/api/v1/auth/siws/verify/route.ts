import { NextRequest } from "next/server";
import { verifySiws } from "@/lib/auth/siws";
import { mintSession, setSessionCookie } from "@/lib/auth/session";
import { fail, json } from "@/lib/auth/require";
import { db } from "@/lib/store/db";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => null)) as
    { wallet?: string; signature?: string; message?: string } | null;
  if (!body?.wallet || !body.signature || !body.message) {
    return fail("MALFORMED_REQUEST", "wallet, signature and message are required", 400);
  }

  const res = await verifySiws(body.wallet, body.signature, body.message);
  if (!res.ok) {
    // Every sign-in failure returns ONE identical answer to the client. The real reason is logged,
    // not published — the difference between "wrong nonce" and "wrong signature" is information an
    // attacker can use and an honest user never needs.
    console.warn("sign-in refused", { reason: res.code, wallet: body.wallet });
    return fail("UNAUTHENTICATED", "sign in to continue", 401);
  }

  // The wallet IS the account: signing in for the first time creates the workspace.
  const d = await db();
  const now = new Date();
  const org = await d.collection("orgs").findOneAndUpdate(
    { owner_wallet: res.wallet },
    {
      $setOnInsert: {
        _id: newOrgId() as never,
        owner_wallet: res.wallet,
        plan: "free",
        active_network: "sandbox", // sandbox-first, always
        created_at: now,
      },
    },
    { upsert: true, returnDocument: "after" },
  );
  if (!org) return fail("INTERNAL", "could not open the workspace", 500);

  await setSessionCookie(mintSession(res.wallet, org._id as unknown as string));
  return json({
    org: { id: org._id, owner_wallet: res.wallet, active_network: org.active_network },
  });
}

// A ULID with the org prefix, matching what the Go side mints.
function newOrgId(): string {
  const A = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let t = Date.now();
  let out = "";
  for (let i = 9; i >= 0; i--) { out = A[t % 32] + out; t = Math.floor(t / 32); }
  for (let i = 0; i < 16; i++) out += A[Math.floor(Math.random() * 32)];
  return `org_${out}`;
}
