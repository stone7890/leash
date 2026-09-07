import { redirect } from "next/navigation";
import { currentSession } from "@/lib/auth/session";
import { SignIn } from "@/components/sign-in";

export const dynamic = "force-dynamic";

export default async function Home() {
  if (await currentSession()) redirect("/agents");
  return <SignIn />;
}
