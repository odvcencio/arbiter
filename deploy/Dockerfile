FROM golang:1.26.10-alpine AS builder
RUN apk add --no-cache git
ENV GOFLAGS=-p=4
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags grammar_blobs_external -ldflags="-s -w" -o /arbiter ./cmd/arbiter
RUN cp "$(go list -m -f '{{.Dir}}' github.com/odvcencio/gotreesitter)/grammars/grammar_blobs/hcl.bin" /hcl.bin

FROM alpine:3.24.3
RUN apk add --no-cache ca-certificates
COPY --from=builder /arbiter /usr/local/bin/arbiter
COPY --from=builder /hcl.bin /usr/local/share/arbiter/grammars/hcl.bin
ENV GOTREESITTER_GRAMMAR_BLOB_DIR=/usr/local/share/arbiter/grammars
RUN addgroup -g 10001 -S arbiter \
  && adduser -u 10001 -S arbiter -G arbiter \
  && mkdir -p /var/lib/arbiter \
  && chown -R arbiter:arbiter /var/lib/arbiter /usr/local/share/arbiter
USER arbiter
EXPOSE 8081
VOLUME ["/var/lib/arbiter"]
ENTRYPOINT ["arbiter"]
CMD ["serve", "--grpc", "0.0.0.0:8081", "--data-dir", "/var/lib/arbiter"]
