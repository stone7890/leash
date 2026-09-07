import { listTemplates } from "@/lib/store/queries";
import { currentSession } from "@/lib/auth/session";
import { Onboarding } from "@/components/onboarding";

export const dynamic = "force-dynamic";

export default async function OnboardingPage() {
  // The session is required to reach this page at all — the layout redirects without one. The
  // owner's wallet is not passed down: the transaction is built server-side against the session's
  // wallet, so the browser cannot ask for a delegation from somebody else's.
  await currentSession();
  const templates = await listTemplates();
  return (
    <Onboarding
      demoHost={demoHost()}
      templates={templates.map((t) => ({
        id: t.id,
        name: t.name,
        description: t.description,
        cap: fmt(t.prefills.cap),
        perTxMax: fmt(t.prefills.perTxMax),
        velocityMax: fmt(t.prefills.velocityMax),
        expiryDays: t.prefills.expiryDays,
        allowHosts: t.prefills.allowHosts,
      }))}
    />
  );
}

// The sample endpoint's host, as the SIGNER will see it.
//
// Step 5 pays this endpoint for real, and S2 compares the host of that request against the agent's
// allow list — so an agent created from a template that does not name it is refused by its own
// onboarding, correctly, at the last step. The host is deployment-specific (`demo402:4200` under
// compose, `localhost:4200` in development), which is why it comes from the environment rather
// than a constant in the template seeds.
function demoHost(): string {
  const raw = process.env.DEMO_ENDPOINT_URL || "http://demo402:4200";
  try {
    return new URL(raw).host;
  } catch {
    return raw.replace(/^https?:\/\//, "");
  }
}

// Money crosses to the client as a STRING, always. A bigint cannot be serialised into props, and
// a number would be an IEEE double — which is exactly what invariant I6 forbids.
function fmt(v: bigint): string {
  const whole = v / 1_000_000n;
  const frac = (v % 1_000_000n).toString().padStart(6, "0");
  return `${whole}.${frac}`;
}
