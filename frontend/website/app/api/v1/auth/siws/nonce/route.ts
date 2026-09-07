import { NextRequest } from "next/server";
import { issueNonce, statement } from "@/lib/auth/siws";
import { fail, json } from "@/lib/auth/require";

export const dynamic = "force-dynamic";

// No session yet — this is how one begins. The wallet is the account.
export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => null)) as { wallet?: string } | null;
  const wallet = body?.wallet?.trim();
  if (!wallet || !/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(wallet)) {
    return fail("INVALID_WALLET", "not a valid Solana address", 422, { field: "wallet" });
  }

  const domain = process.env.SIWS_DOMAIN || "localhost:3000";
  const { nonce, issuedAt } = await issueNonce(wallet);
  return json({
    nonce, issued_at: issuedAt, domain,
    statement: statement(domain, wallet, nonce, issuedAt),
  });
}
