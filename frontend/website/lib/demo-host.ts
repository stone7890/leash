import "server-only";

/**
 * The sample endpoint's host, as the SIGNER will see it.
 *
 * Rule S2 compares the host an agent sends against its allow list, and the test payment sends the
 * host of DEMO_ENDPOINT_URL — `demo402:4200` under compose, `localhost:4200` in development. Two
 * screens need it (the wizard puts it on the allow list, the agent page runs a payment to it), so
 * it is read in one place rather than typed as a constant in two.
 */
export function demoHost(): string {
  const raw = process.env.DEMO_ENDPOINT_URL || "http://demo402:4200";
  try {
    return new URL(raw).host;
  } catch {
    return raw.replace(/^https?:\/\//, "");
  }
}
