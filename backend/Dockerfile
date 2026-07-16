FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/opencharge-api ./cmd/opencharge-api
RUN CGO_ENABLED=0 go build -o /out/opencharge-ingest ./cmd/opencharge-ingest

FROM gcr.io/distroless/static-debian12 AS api
COPY --from=build /out/opencharge-api /usr/local/bin/opencharge-api
ENTRYPOINT ["/usr/local/bin/opencharge-api"]

# Not distroless: the Tesla ingester drives Chromium via chromedp
# (tesla.com/api/findus/* sits behind bot mitigation that rejects plain
# HTTP requests regardless of headers sent — a real browser engine is
# required). This makes the ingest image far heavier than the api one, but
# there's no meaningful way to keep Chromium out of it while that source
# exists. xvfb is needed too: Chromium must run "headed" (Akamai
# fingerprints and denies --headless just like it does plain HTTP), so it's
# launched against a virtual display instead of a real one.
FROM debian:bookworm-slim AS ingest
RUN apt-get update && apt-get install -y --no-install-recommends \
        chromium \
        xvfb \
        xauth \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ENV TESLA_CHROME_PATH=/usr/bin/chromium
COPY --from=build /out/opencharge-ingest /usr/local/bin/opencharge-ingest
ENTRYPOINT ["xvfb-run", "-a", "/usr/local/bin/opencharge-ingest"]
