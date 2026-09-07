"use client";

import { useRouter } from "next/navigation";
import type { ReactNode } from "react";

/**
 * A row in the activity table, clickable across its whole width.
 *
 * It used to be one <Link> around the endpoint text — no underline, a hover colour and nothing
 * else — so a blocked row looked like a dead end. That matters more than it sounds: the recovery
 * buttons for a block live in the detail panel, and the panel only opens from this row. An owner
 * who did not think to click those particular characters had a refusal they could see and no way
 * to act on it.
 *
 * The endpoint keeps a real anchor inside it, so middle-click, ctrl-click and the keyboard still
 * work; this adds the pointer and the hover to everything else.
 */
export function ActivityRow({ href, selected, children }: {
  href: string; selected: boolean; children: ReactNode;
}) {
  const router = useRouter();
  return (
    <tr
      onClick={() => router.push(href)}
      className={`cursor-pointer border-t border-line transition-colors hover:bg-panel2 ${
        selected ? "bg-panel2" : ""}`}
    >
      {children}
    </tr>
  );
}
