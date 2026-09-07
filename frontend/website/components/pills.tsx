import { clsx } from "clsx";
import type { AllowanceState, PaymentState, Tier } from "@/lib/domain/state";
import { ALLOWANCE_TONE, PAYMENT_TONE } from "@/lib/domain/state";

// Colour is NEVER the only carrier. Every pill states its word, so the state survives a monochrome
// screen, a colour-blind reader, and a screenshot in a support ticket.

const TONE = {
  ok:   "bg-grnd text-grn border-grn/30",
  wait: "bg-panel2 text-amb border-amb/30",
  bad:  "bg-redd text-red border-red/30",
} as const;

export function Pill({ tone, children }: {
  tone: keyof typeof TONE; children: React.ReactNode;
}) {
  return (
    <span className={clsx(
      "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
      TONE[tone],
    )}>
      {children}
    </span>
  );
}

export function PaymentPill({ state }: { state: PaymentState }) {
  return <Pill tone={PAYMENT_TONE[state]}>{state}</Pill>;
}

export function AllowancePill({ state }: { state: AllowanceState }) {
  return <Pill tone={ALLOWANCE_TONE[state]}>{state}</Pill>;
}

/**
 * The tier badge — invariant I3 in one component.
 *
 * It takes the tier as DATA. There is no default and no constant table here: the adapter decides
 * what it actually enforces, the allowance stores that, and this renders it. Hardcoding would make
 * the interface claim something the adapter had stopped doing.
 */
export function TierBadge({ tier }: { tier: Tier }) {
  const onchain = tier === "onchain";
  return (
    <span className={clsx(
      "inline-flex items-center gap-1.5 text-xs font-medium",
      onchain ? "text-grn" : "text-amb",
    )}>
      <span className={clsx(
        "inline-block h-2 w-2 rounded-full",
        onchain ? "bg-grn" : "border border-amb bg-transparent",
      )} />
      {onchain ? "on-chain" : "signer"}
    </span>
  );
}
