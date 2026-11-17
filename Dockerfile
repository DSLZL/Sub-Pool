# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS backend-build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/sub-pool ./cmd/sub-pool

FROM oven/bun:1.4.4 AS bun
FROM node:26-bookworm-slim AS frontend-build
WORKDIR /src
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
COPY package.json bun.lock ./
COPY contracts/package.json contracts/package.json
COPY web/package.json web/package.json
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY contracts/ contracts/
COPY web/ web/
ENV NEXT_TELEMETRY_DISABLED=1
RUN cd web && bun run build

FROM node:26-bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 SUB_POOL_HOST=0.0.0.0 SUB_POOL_PORT=3000
COPY --from=backend-build --chown=node:node /out/sub-pool ./sub-pool
COPY --from=frontend-build --chown=node:node /src/web/.next/standalone/ ./frontend/
COPY --from=frontend-build --chown=node:node /src/web/.next/static/ ./frontend/web/.next/static/
COPY --chown=node:node start.mjs ./start.mjs
USER node
EXPOSE 3000
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
    CMD ["node", "-e", "fetch('http://127.0.0.1:3000/health',{signal:AbortSignal.timeout(4000)}).then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"]
CMD ["node", "start.mjs"]
