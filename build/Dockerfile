FROM debian:stable-slim 

RUN addgroup agent \
    && adduser -S agent -u 1000 -G agent

RUN apt-get update && apt-get install -y bash && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --chown=agent:agent --chmod=700 configs/config.yaml proxysql-agent /app/

WORKDIR /app

USER agent

ENTRYPOINT ["/app/proxysql-agent"]