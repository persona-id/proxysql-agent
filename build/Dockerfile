FROM debian:stable-slim

RUN groupadd -r agent \
    && useradd -r -u 1000 -g agent agent

RUN apt-get update && apt-get install -y bash && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --chown=agent:agent --chmod=700 configs/example_config.yaml proxysql-agent /app/config.yaml

WORKDIR /app

USER agent

ENTRYPOINT ["/app/proxysql-agent"]