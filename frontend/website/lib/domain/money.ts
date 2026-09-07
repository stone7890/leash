// Money, on the TypeScript side.
//
// The rule is the same as in Go and it is not negotiable: an amount is an integer count of
// micro-USDC, and it is NEVER a JavaScript `number`. `number` is an IEEE double, and a spending cap
// that has been through one is not a spending cap.
//
// So: `bigint` in memory, a decimal string on the wire and on screen, and no arithmetic anywhere
// that could turn one into the other. The cap input in the interface is type="text", not
// type="number", for exactly this reason.
//
// Both runtimes are checked against contracts/money-vectors.json, so the two implementations
// cannot drift.

export const DECIMALS = 6;
export const ONE = 1_000_000n;
export const MAX = 1_000_000_000_000_000n; // $1,000,000,000

export class MoneyError extends Error {}

/** Parse the decimal string the API uses. Strict on purpose. */
export function parseMoney(s: string, { allowZero = false } = {}): bigint {
  const t = s.trim();
  if (t === "") throw new MoneyError("an amount is required");
  // No exponent, no sign, no separators, no currency. The permissiveness people expect from a
  // number parser is exactly what turns "1e3" into a payment nobody intended.
  if (!/^\d+(\.\d{1,6})?$/.test(t)) {
    throw new MoneyError(`"${s}" is not a decimal amount with at most six places`);
  }
  const [whole, frac = ""] = t.split(".");
  const padded = (frac + "0".repeat(DECIMALS)).slice(0, DECIMALS);
  const v = BigInt(whole ?? "0") * ONE + BigInt(padded || "0");
  if (v === 0n && !allowZero) throw new MoneyError("an amount must be greater than zero");
  if (v > MAX) throw new MoneyError("an amount exceeds the maximum");
  return v;
}

/** Always six places, so columns line up and the format is unambiguous. */
export function formatMoney(v: bigint): string {
  const neg = v < 0n;
  const a = neg ? -v : v;
  const whole = a / ONE;
  const frac = (a % ONE).toString().padStart(DECIMALS, "0");
  return `${neg ? "-" : ""}${whole}.${frac}`;
}

/** For display next to a currency symbol: $7.42 rather than $7.420000. */
export function formatShort(v: bigint): string {
  const s = formatMoney(v);
  return s.replace(/(\.\d\d)\d*$/, "$1");
}

/** remaining = cap − drawn − reserved, clamped at zero. */
export function remaining(cap: bigint, drawn: bigint, reserved: bigint): bigint {
  const r = cap - drawn - reserved;
  return r < 0n ? 0n : r;
}

/** How much of the cap has been used, as a whole percent. */
export function percentUsed(cap: bigint, rem: bigint): number {
  if (cap <= 0n) return 0;
  return Number(((cap - rem) * 100n) / cap);
}
