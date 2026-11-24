# PostgreSQL 17 (Alpine) + pgvector, pour la recherche sémantique (phase 3).
# Même base que l'image postgres:17-alpine utilisée jusqu'ici : un volume
# existant se réutilise tel quel (mêmes collations musl).
FROM postgres:17-alpine
ARG PGVECTOR_VERSION=0.8.1
RUN apk add --no-cache --virtual .build-deps git build-base \
 && git clone --depth 1 --branch "v${PGVECTOR_VERSION}" https://github.com/pgvector/pgvector.git /tmp/pgvector \
 && make -C /tmp/pgvector OPTFLAGS="" with_llvm=no \
 && make -C /tmp/pgvector with_llvm=no install \
 && rm -rf /tmp/pgvector \
 && apk del .build-deps
