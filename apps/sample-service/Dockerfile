FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ .
# The build is static so it can run on distroless. The version string comes from
# APP_VERSION at runtime, set by the chart from the image tag, which keeps the
# image content identical for a given commit no matter what it is tagged.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app /app

EXPOSE 8080

ENTRYPOINT ["/app"]
