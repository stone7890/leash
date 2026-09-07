import "server-only";
import { NextResponse } from "next/server";
import { currentSession } from "./session";

// Authentication is an explicit FIRST STATEMENT in every route handler, never a middleware.
//
// A middleware you forgot to attach fails open. A first line you forgot to write fails the build,
// because dependency-cruiser and the route lint assert this shape.

export type Caller = { wallet: string; org: string };

export class Unauthenticated extends Error {}

export async function requireOwner(): Promise<Caller> {
  const s = await currentSession();
  if (!s) throw new Unauthenticated("sign in to continue");
  return { wallet: s.wallet, org: s.org };
}

/** The one envelope every endpoint uses on an error path. */
export function fail(
  code: string, message: string, status: number,
  extra: { field?: string; details?: unknown; retriable?: boolean } = {},
) {
  return NextResponse.json(
    {
      error: {
        code, message,
        ...(extra.field ? { field: extra.field } : {}),
        ...(extra.details ? { details: extra.details } : {}),
        // Always present, even when false: an SDK must not infer retriability from the code.
        retriable: extra.retriable ?? false,
      },
    },
    { status },
  );
}

export function unauthenticated() {
  return fail("UNAUTHENTICATED", "sign in to continue", 401);
}

/** Money leaves as a STRING. JSON numbers are IEEE doubles. */
export function json(body: unknown, status = 200) {
  return new NextResponse(
    JSON.stringify(body, (_k, v) => (typeof v === "bigint" ? formatBig(v) : v)),
    { status, headers: { "content-type": "application/json" } },
  );
}

function formatBig(v: bigint): string {
  const neg = v < 0n;
  const a = neg ? -v : v;
  const whole = a / 1_000_000n;
  const frac = (a % 1_000_000n).toString().padStart(6, "0");
  return `${neg ? "-" : ""}${whole}.${frac}`;
}
