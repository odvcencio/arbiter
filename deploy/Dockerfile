FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
ENV GONOSUMCHECK=github.com/odvcencio/* GONOSUMDB=github.com/odvcencio/*
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /arbiter ./cmd/arbiter

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /arbiter /usr/local/bin/arbiter
RUN addgroup -S arbiter && adduser -S arbiter -G arbiter
USER arbiter
EXPOSE 8081
ENTRYPOINT ["arbiter"]
CMD ["serve", "--grpc", ":8081"]
