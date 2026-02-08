# Build stage
FROM golang:latest AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -installsuffix cgo -ldflags="-w -s" -o torrProxy .

FROM scratch

COPY --from=builder /app/torrProxy /torrProxy

# Expose the default port
EXPOSE 8080

# Run the application
ENTRYPOINT ["/torrProxy"]