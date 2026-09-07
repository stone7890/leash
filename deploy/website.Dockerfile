# The dashboard, and the fifteen API routes it serves.
FROM node:24-bookworm-slim AS deps
WORKDIR /app
COPY frontend/website/package.json frontend/website/package-lock.json* ./
RUN npm install --ignore-scripts

FROM node:24-bookworm-slim AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY frontend/website/ ./
COPY contracts/ /contracts/
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:24-bookworm-slim AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000
# Next's standalone output carries only the files the server actually needs.
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
COPY contracts/ /contracts/
# The self-serve revoke guide is served from the repository's own file rather than retyped into a
# component. A second copy is a copy that goes stale, and this is the one page whose incorrectness
# costs a customer money.
COPY docs/if-leash-is-down.md /docs/if-leash-is-down.md
USER node
EXPOSE 3000
CMD ["node", "server.js"]
