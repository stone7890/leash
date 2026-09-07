/** @type {import('next').NextConfig} */
const nextConfig = {
  // standalone carries only the files the server actually needs, which is what the runtime image
  // copies.
  output: "standalone",
  reactStrictMode: true,
  // There is deliberately no NEXT_PUBLIC_* variable anywhere in this app. Everything is read
  // server-side at runtime, so changing an endpoint does not require a rebuild.
  env: {},

  // The contract puts every endpoint under /v1; Next serves route handlers from app/api. In
  // production nginx joins the two — but in development there is no nginx, and without these the
  // dashboard's own calls would 404 against a server that is running perfectly.
  //
  // Harmless in production: nginx matches /v1/sign and the SSE stream first and never forwards
  // them here, so these only ever see what it hands over.
  async rewrites() {
    return {
      beforeFiles: [
        {
          // The onboarding stream lives in the indexer: it needs a resumable change stream held
          // open for a whole wizard session, which is why it is not a route handler.
          source: "/v1/test-payments/:id/stream",
          destination: `${process.env.INDEXER_BASE_URL || "http://localhost:4300"}/v1/test-payments/:id/stream`,
        },
      ],
      afterFiles: [
        { source: "/v1/:path*", destination: "/api/v1/:path*" },
      ],
      fallback: [],
    };
  },
};
export default nextConfig;
