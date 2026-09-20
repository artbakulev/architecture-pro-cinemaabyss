FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -o events-service .

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/events-service .

EXPOSE 8082

CMD ["./events-service"]
