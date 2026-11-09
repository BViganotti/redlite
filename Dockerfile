FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .

ARG COMMIT=dev
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/BViganotti/redlite/internal/version.Commit=${COMMIT}" \
    -o /out/redlite ./cmd/redlite

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/redlite /usr/local/bin/redlite

USER nonroot:nonroot
EXPOSE 6379
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/redlite"]
CMD ["--bind", "0.0.0.0", "--appendonly", "--dir", "/data"]
