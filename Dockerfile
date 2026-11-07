FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cursor-proxy ./cmd/server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/cursor-proxy /usr/local/bin/cursor-proxy
EXPOSE 3100
ENTRYPOINT ["/usr/local/bin/cursor-proxy"]
