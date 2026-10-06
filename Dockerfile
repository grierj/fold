# Build stage
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
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
