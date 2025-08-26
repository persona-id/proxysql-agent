# syntax=docker/dockerfile:1

# This file is used by GoReleaser to build the Docker image

FROM debian:stable-slim

RUN groupadd -r agent \
    && useradd -r -u 1000 -g agent agent

RUN apt-get update \
    && apt-get install -y bash \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY --chown=agent:agent --chmod=700 config.yaml proxysql-agent /app/config.yaml

WORKDIR /app

USER agent

ENTRYPOINT ["/app/proxysql-agent"]