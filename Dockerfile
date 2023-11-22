# syntax=docker/dockerfile:1

# Stage 1
FROM golang:1.21.4-alpine AS builder

ARG BUILD_SHA
ARG BUILD_TIME
ARG VERSION

ENV GO111MODULE=on

# Set destination for COPY
WORKDIR /build

COPY go.sum go.mod .

RUN go mod download

COPY . .

RUN apk update && apk add --no-cache git && rm -rf /var/cache/apk/*

RUN CGO_ENABLED="0" go build -ldflags "-s -w -X 'main.Version=${VERSION}' -X 'main.Build=${BUILD_SHA}' -X 'main.BuildTime=${BUILD_TIME}'" -o proxysql-agent .

# Stage 2
FROM alpine:3.18.4 as runner

# add mysql-client to apk add when we're ready
RUN apk update \
    && apk add --no-cache bash bind-tools \
    && rm -rf /var/cache/apk/* \
    && addgroup agent \
    && adduser -S agent -u 1000 -G agent

WORKDIR /app

COPY --chown=agent:agent --from=builder --chmod=700 /build/proxysql-agent /app/
COPY --chown=agent:agent --from=builder --chmod=600 /build/example_config.yaml /app/config.yaml

USER agent

ENTRYPOINT ["/proxysql-agent"]
