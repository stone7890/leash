import "server-only";
import { MongoClient, Db, Long } from "mongodb";

// The ONLY module in this app that imports the MongoDB driver.
//
// `app/**` must never import it directly — that is the most likely accident, given how easy a route
// handler makes it, and dependency-cruiser fails the build on it. Nothing here returns a driver
// type either: callers get plain objects, so they cannot construct a query.
//
// The Go services have their own access layer, and that duplication is deliberate and registered
// (docs/16-deck-conformance.md §D-6). It is survivable because the ENFORCEMENT POINT IS THE
// SERVER: the validators, the partial unique indexes and the role privileges constrain both
// runtimes identically, and `verify-constraints` proves them against MongoDB rather than a client.

declare global {
  // eslint-disable-next-line no-var
  var __leashMongo: Promise<MongoClient> | undefined;
}

function uri(): string {
  const v = process.env.MONGO_URI;
  if (!v) {
    // Secrets and connection strings are never defaulted. A default here would be a service that
    // starts, looks healthy, and talks to the wrong database.
    throw new Error("MONGO_URI is not set. See docs/14-deployment.md");
  }
  if (!v.includes("replicaSet=") && !v.startsWith("mongodb+srv://")) {
    throw new Error(
      "MONGO_URI does not name a replica set. Leash has no standalone mode: transactions and " +
      "change streams both require one.",
    );
  }
  return v;
}

/** The client is cached on globalThis so a dev hot-reload does not open a new pool each time. */
export function client(): Promise<MongoClient> {
  if (!global.__leashMongo) {
    global.__leashMongo = new MongoClient(uri(), {
      // A money write acknowledged by one node and lost in a failover is a signature we handed out
      // and cannot account for.
      writeConcern: { w: "majority" },
      readConcern: { level: "majority" },
      retryWrites: true,
      appName: "leash-website",
      // WITHOUT THIS, the driver returns BSON int64 as a JavaScript `number`, silently, and every
      // amount above 2^53 loses precision on the way out of the database. The guard in toBig()
      // catches it — it caught it here — but the right fix is not to let it happen: promoteLongs
      // false keeps a Long, which converts to bigint exactly.
      //
      // This is invariant I6 at the one boundary where the language would break it by default.
      promoteLongs: false,
    }).connect();
  }
  return global.__leashMongo;
}

export async function db(): Promise<Db> {
  const c = await client();
  return c.db(process.env.MONGO_DB || "leash");
}

/**
 * Money crosses this boundary as BSON int64 and becomes a bigint. Never a JS number: `number` is
 * an IEEE double, and reading a cap through one is how a spending control stops being exact.
 */
export function toBig(v: unknown): bigint {
  if (v instanceof Long) return BigInt(v.toString());
  if (typeof v === "bigint") return v;
  if (typeof v === "number") {
    // Reaching here means promoteLongs was re-enabled, or a document was written through a path
    // that bypassed the schema. Either way the value may already be wrong, and rounding it into
    // place would destroy the evidence. Loud, not coerced.
    throw new Error(
      "an amount was read as a JavaScript number — money has lost precision. Check that the " +
      "Mongo client still sets promoteLongs: false",
    );
  }
  if (typeof v === "string") return BigInt(v);
  if (v === null || v === undefined) return 0n;
  throw new Error(`an amount was read as ${typeof v}, which is not an integer`);
}

/** And back the other way, so a write is int64 and never a double. */
export function toLong(v: bigint): Long {
  return Long.fromBigInt(v);
}
