# Frontend build stage: the SPA bundle is embedded into the Go binary.
FROM node:20-alpine AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Backend build stage
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly built SPA so go:embed picks it up.
COPY --from=webbuild /web/dist/ ./web/dist/
# Pure-Go SQLite driver (modernc via glebarez) => no CGO needed.
RUN CGO_ENABLED=0 go build -trimpath -o /out/church-groups .

# Run stage
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/church-groups /church-groups
COPY --from=build /src/templates /templates
COPY --from=build /src/static /static
ENV PORT=8080 \
    APP_ENV=prod \
    SQLITE_PATH=/data/church-groups.db
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/church-groups"]
