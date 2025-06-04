FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${APP_VERSION}" -o /app .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app /app

EXPOSE 8080

ENTRYPOINT ["/app"]
