FROM golang:1.24-alpine AS builder
RUN apk add --no-cache build-base
WORKDIR /app
ARG APP_VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags "-X main.AppVersion=${APP_VERSION}" -o /out/api-web-tgbot .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /opt/api-web-tgbot
COPY --from=builder /out/api-web-tgbot ./api-web-tgbot
COPY web ./web
ENV DATA_DIR=/data/api-web-tgbot
ENV PORT=8088
EXPOSE 8088
ENTRYPOINT ["./api-web-tgbot"]
