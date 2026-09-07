import "server-only";
import { createHmac, timingSafeEqual, randomBytes } from "node:crypto";
import { cookies } from "next/headers";

// The owner's session.
//
// The wallet IS the account: no password, no email address, nothing to phish and nothing to leak
// in a breach. What is stored is a signed token naming the wallet, in an httpOnly cookie.
//
// Written by hand rather than with a JWT library, for the reason the sibling project gives: a
// library brings an `alg` header a caller can set to `none`, and the whole class of
// algorithm-confusion vulnerabilities that follows. This verifies HMAC-SHA256 unconditionally and
// has no header to confuse.

const COOKIE = "leash_session";
const SESSION_SECONDS = 8 * 60 * 60;

function key(): Buffer {
  const v = process.env.SESSION_KEY;
  if (!v || v.length < 32) {
    // Never defaulted. A default secret is a secret in production, and a short one is not a secret
    // at all.
    throw new Error("SESSION_KEY is not set, or is shorter than 32 characters");
  }
  return Buffer.from(v, "utf8");
}

type Claims = { wallet: string; org: string; exp: number };

function sign(payload: string): string {
  return createHmac("sha256", key()).update(payload).digest("base64url");
}

export function mintSession(wallet: string, org: string): string {
  const claims: Claims = {
    wallet, org, exp: Math.floor(Date.now() / 1000) + SESSION_SECONDS,
  };
  const payload = Buffer.from(JSON.stringify(claims), "utf8").toString("base64url");
  return `${payload}.${sign(payload)}`;
}

/**
 * Verify the signature BEFORE parsing the payload.
 *
 * The order matters: parsing attacker-controlled JSON before authenticating it widens the attack
 * surface to the parser itself.
 */
export function readSession(token: string | undefined): Claims | null {
  if (!token) return null;
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return null;
  const payload = token.slice(0, dot);
  const mac = token.slice(dot + 1);

  const expected = sign(payload);
  const a = Buffer.from(mac);
  const b = Buffer.from(expected);
  if (a.length !== b.length || !timingSafeEqual(a, b)) return null;

  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as Claims;
    if (!claims.wallet || !claims.org) return null;
    if (claims.exp * 1000 < Date.now()) return null;
    return claims;
  } catch {
    return null;
  }
}

export async function setSessionCookie(token: string) {
  const jar = await cookies();
  jar.set(COOKIE, token, {
    httpOnly: true,
    sameSite: "lax",
    // The cookie is not marked Secure in development, where the demo runs over plain HTTP behind
    // nginx. Behind TLS it should be.
    secure: process.env.NODE_ENV === "production" && process.env.HTTPS !== "false",
    path: "/",
    maxAge: SESSION_SECONDS,
  });
}

export async function clearSessionCookie() {
  const jar = await cookies();
  jar.delete(COOKIE);
}

export async function currentSession(): Promise<Claims | null> {
  const jar = await cookies();
  return readSession(jar.get(COOKIE)?.value);
}

export function newNonce(): string {
  return randomBytes(24).toString("base64url");
}
