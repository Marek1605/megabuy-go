# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/main.go

# Final stage
FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /app/main .
COPY --from=builder /app/migrations ./migrations
RUN mkdir -p /app/uploads
EXPOSE 8080
ENV PORT=8080
CMD ["./main"]
