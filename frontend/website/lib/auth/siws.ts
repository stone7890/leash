import "server-only";
import nacl from "tweetnacl";
import bs58 from "bs58";
import { db } from "@/lib/store/db";
import { newNonce } from "./session";

// Sign-In With Solana.
//
// THE NONCE STORE IS NOT IN THE SPECIFICATION, and its absence is a real gap rather than an
// oversight we inherited: without one, a signed sign-in message is replayable for as long as it
// exists, and anyone who observes it can present it again. Registered in
// docs/16-deck-conformance.md §D-2.
//
// A nonce is issued, stored, and consumed by DELETION — read-and-delete in one operation, so a
// replay finds nothing at all rather than finding a used marker somebody forgot to check.

const NONCE_TTL_MS = 5 * 60 * 1000;

export function statement(domain: string, wallet: string, nonce: string, issuedAt: string): string {
  // What the wallet shows the owner. It says what signing means, because a signature request with
  // no explanation trains people to approve anything.
  return [
    `${domain} wants you to sign in with your Solana account:`,
    wallet,
    "",
    "Signing proves you control this wallet. It does not move any funds and does not grant Leash",
    "access to them.",
    "",
    `URI: https://${domain}`,
    "Version: 1",
    `Nonce: ${nonce}`,
    `Issued At: ${issuedAt}`,
  ].join("\n");
}

export async function issueNonce(wallet: string) {
  const d = await db();
  const nonce = newNonce();
  const now = new Date();
  await d.collection("siws_nonces").insertOne({
    _id: nonce as never,
    wallet,
    issued_at: now,
    expires_at: new Date(now.getTime() + NONCE_TTL_MS),
  });
  return { nonce, issuedAt: now.toISOString() };
}

export type VerifyResult =
  | { ok: true; wallet: string }
  | { ok: false; code: "NONCE_ALREADY_USED" | "NONCE_EXPIRED" | "INVALID_SIGNATURE" };

/**
 * Consume the nonce, then check the signature.
 *
 * The nonce is deleted first and unconditionally: a failed verification still spends it, or an
 * attacker could grind signatures against one nonce until something worked.
 */
export async function verifySiws(
  wallet: string, signatureB58: string, message: string,
): Promise<VerifyResult> {
  const d = await db();

  const nonce = extractNonce(message);
  if (!nonce) return { ok: false, code: "INVALID_SIGNATURE" };

  const consumed = await d.collection("siws_nonces").findOneAndDelete({
    _id: nonce as never, wallet,
  });
  if (!consumed) return { ok: false, code: "NONCE_ALREADY_USED" };
  if (new Date(consumed.expires_at as Date).getTime() < Date.now()) {
    return { ok: false, code: "NONCE_EXPIRED" };
  }

  try {
    const ok = nacl.sign.detached.verify(
      new TextEncoder().encode(message),
      bs58.decode(signatureB58),
      bs58.decode(wallet),
    );
    return ok ? { ok: true, wallet } : { ok: false, code: "INVALID_SIGNATURE" };
  } catch {
    return { ok: false, code: "INVALID_SIGNATURE" };
  }
}

function extractNonce(message: string): string | null {
  const m = /^Nonce: (.+)$/m.exec(message);
  return m?.[1]?.trim() ?? null;
}
