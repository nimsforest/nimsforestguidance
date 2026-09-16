FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 guidance
COPY nimsforestguidance /usr/local/bin/nimsforestguidance
USER guidance
ENV DATA_DIR=/data
ENTRYPOINT ["nimsforestguidance"]
CMD ["serve"]
