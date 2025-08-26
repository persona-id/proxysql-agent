# syntax=docker/dockerfile:1

# This file is used by the devcontainer to build the Docker image, and is NOT used by GoReleaser

# Stage 1
FROM golang:1.25.0-alpine AS builder

ARG BUILD_SHA
ARG BUILD_TIME
ARG VERSION

ENV GO111MODULE=on

# Set destination for COPY
WORKDIR /build

COPY go.sum go.mod ./

RUN go mod download

COPY . .

RUN CGO_ENABLED="0" go build -ldflags "-s -w" -o proxysql-agent cmd/proxysql-agent/main.go

# Stage 2
FROM alpine:3.18.4 AS runner

RUN addgroup agent \
    && adduser -S agent -u 1000 -G agent

# add mysql-client, curl, jq, etc to apk add when we're ready
RUN apk add --no-cache bash=5.2.15-r5

WORKDIR /app

COPY --chown=agent:agent --from=builder --chmod=700 /build/proxysql-agent /app/proxysql-agent
COPY --chown=agent:agent --from=builder --chmod=600 /build/config.yaml /app/config.yaml

USER agent

ENTRYPOINT ["/app/proxysql-agent"]