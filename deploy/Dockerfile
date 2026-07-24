# Build the backend (with the SPA already embedded via web/dist) and ship it in a
# minimal runtime image. CGO_ENABLED=0 + modernc.org/sqlite means the binary is
# fully static, so the runtime stage needs no libc — it runs identically on
# Alpine/musl and Debian/glibc hosts (PLAN.md §3).
FROM golang:1.26-alpine AS build
WORKDIR /src
# Modules first so dependency downloads cache across source edits.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-s -w -X main.Version=${VERSION}" \
      -o /out/fabscreentimed ./cmd/fabscreentimed

FROM alpine:3.22
# ca-certificates: outbound HTTPS (health pings/webhooks). sqlite: the nightly
# .backup job runs `sqlite3` inside this container. tzdata: household timezone.
RUN apk add --no-cache ca-certificates sqlite tzdata \
    && addgroup -S -g 10001 fabscreentime \
    && adduser -S -u 10001 -G fabscreentime -H -s /sbin/nologin fabscreentime
COPY --from=build /out/fabscreentimed /usr/local/bin/fabscreentimed
# Data (SQLite) and the signed agent release directory are volumes.
RUN mkdir -p /var/lib/fabscreentime /srv/agent \
    && chown -R fabscreentime:fabscreentime /var/lib/fabscreentime /srv/agent
USER fabscreentime
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fabscreentimed"]
CMD ["-addr", ":8080", "-db", "/var/lib/fabscreentime/data.db", "-agentdir", "/srv/agent"]
