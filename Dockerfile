# Frontend build stage: the SPA bundle is embedded into the Go binary.
FROM node:20-alpine AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Backend build stage
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly built SPA so go:embed picks it up.
COPY --from=webbuild /web/dist/ ./web/dist/
# Pure-Go SQLite driver (modernc via glebarez) => no CGO needed.
RUN CGO_ENABLED=0 go build -trimpath -o /out/fold .
# Seed the data dir so the named volume inherits nonroot ownership on
# first mount (distroless nonroot is 65532:65532; there is no shell to
# chown with in the final stage).
RUN mkdir -p /data/uploads

# Run stage
FROM gcr.io/distroless/static-debian12:nonroot
# Pin the working directory: the binary uses relative paths for templates/
# and static assets, and distroless does not guarantee WORKDIR=/.
WORKDIR /
COPY --from=build /out/fold /fold
COPY --from=build /src/templates /templates
COPY --from=build /src/static /static
# Carry the data dir (with nonroot ownership) into the image so a fresh
# named volume starts writable by the app user.
COPY --from=build --chown=65532:65532 /data /data
ENV PORT=8080 \
    APP_ENV=prod \
    SQLITE_PATH=/data/fold.db \
    UPLOAD_DIR=/data/uploads
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/fold"]
# Distroless has no shell or curl. The same binary probes /healthz.
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=5 \
  CMD ["/fold", "healthcheck"]
