FROM alpine:3.18

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