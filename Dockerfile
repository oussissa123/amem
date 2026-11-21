# Image du serveur amem : binaire unique (CLI, serveur MCP stdio, serveur HTTP).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0-dev
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/amem ./cmd/amem

FROM alpine:3.22
# git : résolution du projet par dépôt quand la CLI est utilisée dans le conteneur.
RUN apk add --no-cache ca-certificates git wget \
 && adduser -D -u 10001 amem \
 && mkdir -p /data/projects && chown -R amem:amem /data
COPY --from=build /out/amem /usr/local/bin/amem
USER amem
ENV AMEM_ROOT=/data/projects \
    AMEM_LISTEN=0.0.0.0:7420
VOLUME /data
EXPOSE 7420
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:7420/readyz || exit 1
ENTRYPOINT ["amem"]
CMD ["serve"]
