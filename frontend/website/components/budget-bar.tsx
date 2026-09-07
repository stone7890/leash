import { clsx } from "clsx";
import { formatShort, percentUsed } from "@/lib/domain/money";

/**
 * The budget bar. Red at 95% or more, per the interface specification.
 *
 * Amounts are strings throughout. No value here has ever been a JavaScript `number`.
 */
export function BudgetBar({ cap, remaining }: { cap: bigint; remaining: bigint }) {
  const pct = percentUsed(cap, remaining);
  const spent = cap - remaining;
  return (
    <div className="w-full">
      <div className="mono mb-1 flex justify-between text-xs">
        <span className="text-ink">${formatShort(spent)} of ${formatShort(cap)}</span>
        <span className={clsx(pct >= 95 ? "text-red" : "text-mut")}>{pct}%</span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-panel2">
        <div
          className={clsx("h-full rounded-full", pct >= 95 ? "bg-red" : "bg-grn")}
          style={{ width: `${Math.min(100, Math.max(0, pct))}%` }}
        />
      </div>
    </div>
  );
}
