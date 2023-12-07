FROM alpine:3.18

RUN addgroup agent \
    && adduser -S agent -u 1000 -G agent

RUN apk add --no-cache bash=5.2.15-r5

COPY --chown=agent:agent --chmod=700 configs/config.yaml proxysql-agent /app/

WORKDIR /app

USER agent

ENTRYPOINT ["/app/proxysql-agent"]