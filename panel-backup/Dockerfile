FROM alpine:3.19
RUN apk add --no-cache bash mysql-client jq tar
COPY backup.sh /usr/local/bin/backup.sh
RUN chmod +x /usr/local/bin/backup.sh
ENTRYPOINT ["/usr/local/bin/backup.sh"]
