# syntax=docker/dockerfile:1

# Stage 1
FROM golang:1.21.4-alpine AS builder

ARG BUILD_SHA
ARG BUILD_TIME
ARG VERSION

ENV GO111MODULE=on

# Set destination for COPY
WORKDIR /build

COPY go.sum go.mod ./

RUN go mod download

COPY . .

RUN apk update \
    && apk add --no-cache git=2.40.1-r0 \
    && rm -rf /var/cache/apk/* /lib/apk/db/*

RUN CGO_ENABLED="0" go build -ldflags "-s -w -X 'main.version=${VERSION}' -X 'main.build=${BUILD_SHA}' -X 'main.builddate=${BUILD_TIME}'" -o proxysql-agent cmd/proxysql-agent/main.go

# Stage 2
FROM alpine:3.18.4 as runner

# add mysql-client, curl, jq, etc to apk add when we're ready
RUN apk update \
    && apk add --no-cache bash=5.2.15-r5 \
    && rm -rf /var/cache/apk/* \
    && addgroup agent \
    && adduser -S agent -u 1000 -G agent

WORKDIR /app

COPY --chown=agent:agent --from=builder --chmod=700 /build/proxysql-agent /app/
COPY --chown=agent:agent --from=builder --chmod=600 /build/configs/example_config.yaml /app/config.yaml

USER agent

ENTRYPOINT ["/app/proxysql-agent"]
