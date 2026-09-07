import { clsx } from "clsx";
import { COPY } from "@/lib/domain/fault";
import { PHASE_LABEL, PHASE_ORDER, type PaymentState, type Phase } from "@/lib/domain/state";
import type { SignRequest, TimelineEvent } from "@/lib/store/queries";

/**
 * The six-phase handshake.
 *
 * Three presentations, and they are genuinely different because the states are:
 *
 *   confirmed — every phase that happened, with the slot the chain was read at
 *   blocked   — stops at phase 2, naming the rule, and says the rest were NOT REACHED
 *   unknown   — phases up to signing, then a spinner and the fixed copy
 *
 * Phase 4, `replayed`, happens inside the customer's agent runtime, which we do not run and
 * cannot observe. When it is absent it renders dimmed — never as a failure, and never with an
 * invented timestamp, because fabricating a row would be inventing evidence in an audit trail.
 */
export function Timeline({ phases, state, verdict }: {
  phases: TimelineEvent[];
  state: PaymentState;
  verdict: SignRequest | null;
}) {
  const seen = new Map<Phase, TimelineEvent>(phases.map((p) => [p.phase, p]));
  const blocked = verdict?.verdict === "blocked";
  const reachedIndex = blocked ? 1 : PHASE_ORDER.length - 1;

  return (
    <div className="space-y-2">
      {PHASE_ORDER.map((phase, i) => {
        const event = seen.get(phase);
        const notReached = blocked && i > reachedIndex;
        // Unobservable rather than missing: a different thing, and shown as such.
        const unobservable = phase === "replayed" && !event && !blocked;

        return (
          <div key={phase} className="flex gap-3 text-xs">
            <span className={clsx(
              "mt-1 inline-block h-2 w-2 shrink-0 rounded-full",
              event ? "bg-grn" : notReached ? "bg-line" : "border border-mut",
            )} />
            <div className="min-w-0 flex-1">
              <div className={clsx(
                event ? "text-ink" : unobservable ? "text-mut/50" : "text-mut",
              )}>
                {i + 1}. {PHASE_LABEL[phase]}
                {unobservable && " — happens inside your agent"}
              </div>
              {event && (
                <div className="mono mt-0.5 text-[11px] text-mut">
                  {event.at.slice(11, 23)}
                  {event.readBackSlot ? ` · slot ${event.readBackSlot}` : ""}
                  {renderDetail(event)}
                </div>
              )}
              {notReached && i === reachedIndex + 1 && (
                <div className="mt-0.5 text-[11px] text-mut">
                  {PHASE_ORDER.length - reachedIndex - 1} checks not reached
                </div>
              )}
            </div>
          </div>
        );
      })}

      {blocked && verdict?.failedRule && (
        <div className="mt-3 rounded-[10px] border border-red/30 bg-redd p-3">
          <div className="text-xs font-medium text-red">
            Blocked by rule {verdict.failedRule}
          </div>
          <p className="mt-1 text-[11px] text-mut">
            Result: blocked — {COPY.nothingMoved}.
          </p>
        </div>
      )}

      {state === "unknown" && (
        <div className="mt-3 rounded-[10px] border border-amb/30 bg-panel2 p-3">
          <div className="text-xs font-medium text-amb">Outcome unknown</div>
          <p className="mt-1 text-[11px] text-mut">{COPY.unknownPayment}</p>
        </div>
      )}

      {verdict && (
        // The count comes from the ARRAY, never a literal. That is what stops the interface's
        // "N of N" drifting from the engine — the specification's own sources disagreed about
        // whether it was six or seven, and there are eight rules.
        <p className="mt-3 text-[11px] text-mut">
          {verdict.checks.filter((c) => c.result === "pass").length} of {verdict.checks.length}{" "}
          checks passed
        </p>
      )}
    </div>
  );
}

function renderDetail(e: TimelineEvent): string {
  if (!e.detail) return "";
  const d = e.detail as Record<string, unknown>;
  if (typeof d.amount === "string") return ` · $${d.amount}`;
  if (typeof d.passed === "number") return ` · ${d.passed}/${d.total} checks`;
  if (typeof d.allowance === "string") {
    return ` · ${String(d.allowance).slice(0, 6)}…`;
  }
  return "";
}
