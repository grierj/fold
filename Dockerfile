# The image a church runs. Built by the release workflow and pulled by the
# church's server; nothing is compiled there (docs/HAZARDS.md section 8).
FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fold ./cmd/fold \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fold /fold
# An empty /data owned by the nonroot user, so a new named volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
ENV DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/fold"]
CMD ["serve"]
