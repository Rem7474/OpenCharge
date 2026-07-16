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

FROM gcr.io/distroless/static-debian12 AS ingest
COPY --from=build /out/opencharge-ingest /usr/local/bin/opencharge-ingest
ENTRYPOINT ["/usr/local/bin/opencharge-ingest"]
