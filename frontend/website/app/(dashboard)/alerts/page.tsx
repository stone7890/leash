import { currentSession } from "@/lib/auth/session";
import { listAlerts } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

const KIND_COPY: Record<string, string> = {
  budget_warning:  "reached 80% of its cap",
  budget_critical: "reached 95% of its cap",
  blocked:         "was blocked by a rule",
  new_endpoint:    "paid an endpoint it has not paid before",
  expiring_soon:   "expires within 24 hours",
  payment_failed:  "left a payment with no trace for 24 hours",
};

export default async function AlertsPage() {
  const session = (await currentSession())!;
  const alerts = await listAlerts(session.org, 50);

  return (
    <div className="max-w-3xl space-y-6">
      <section className="card">
        <h2 className="text-sm font-medium">When Leash tells you</h2>
        <ul className="mt-3 space-y-1.5 text-xs text-mut">
          <li>· A budget crosses 80%, and again at 95%</li>
          <li>· A payment is blocked by a rule</li>
          <li>· An agent pays an endpoint it has never paid before</li>
          <li>· An allowance expires within 24 hours</li>
        </ul>
        <p className="mt-3 text-xs text-mut">
          Alerts fire from the indexer within 30 seconds of the on-chain event they describe.
        </p>
      </section>

      <section>
        <h2 className="mb-3 text-sm font-medium text-mut">Recent alerts</h2>
        {alerts.length === 0 ? (
          <div className="card text-xs text-mut">
            Nothing yet. That is the normal state.
          </div>
        ) : (
          <div className="overflow-hidden rounded-[10px] border border-line">
            <table className="w-full text-sm">
              <thead className="bg-panel text-left text-xs text-mut">
                <tr>
                  <th className="px-4 py-2 font-medium">Time</th>
                  <th className="px-4 py-2 font-medium">Alert</th>
                </tr>
              </thead>
              <tbody>
                {alerts.map((a) => (
                  <tr key={a.id} className="border-t border-line">
                    <td className="mono px-4 py-2 text-xs text-mut">
                      {a.createdAt.slice(0, 16).replace("T", " ")}
                    </td>
                    <td className="px-4 py-2 text-xs">
                      <span className="text-ink">{String(a.body.agent ?? "an agent")}</span>{" "}
                      <span className="text-mut">{KIND_COPY[a.kind] ?? a.kind}</span>
                      {a.body.spent != null && (
                        <span className="mono text-mut">
                          {" "}(${String(a.body.spent)} of ${String(a.body.cap)})
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
