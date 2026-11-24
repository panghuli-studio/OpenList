# Same runtime layers as upstream's Dockerfile.ci (base image, user, paths, entrypoint), so
# upstream's docker docs, volumes and env vars apply unchanged. The binary is cross-compiled
# on the build platform with CGO off: the default sqlite driver (glebarez/sqlite) is pure Go,
# and this avoids upstream's musl toolchain download and QEMU-emulated compiles.
ARG BASE_IMAGE_TAG=base

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG WEB_VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILT_AT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# public/dist must already hold the front-end; the workflow unpacks the pinned release there.
RUN test -f public/dist/index.html
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags=jsoniter -o /out/openlist \
    -ldflags="-w -s \
      -X 'github.com/OpenListTeam/OpenList/v4/internal/conf.BuiltAt=${BUILT_AT}' \
      -X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitAuthor=The OpenList Projects Contributors <noreply@oplist.org>' \
      -X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitCommit=${GIT_COMMIT}' \
      -X 'github.com/OpenListTeam/OpenList/v4/internal/conf.Version=${VERSION}' \
      -X 'github.com/OpenListTeam/OpenList/v4/internal/conf.WebVersion=${WEB_VERSION}'" \
    .

FROM ghcr.io/openlistteam/openlist-base-image:${BASE_IMAGE_TAG}
ARG INSTALL_ARIA2=false
ARG USER=openlist
ARG UID=1001
ARG GID=1001

WORKDIR /opt/openlist/

RUN addgroup -g ${GID} ${USER} && \
    adduser -D -u ${UID} -G ${USER} ${USER} && \
    mkdir -p /opt/openlist/data

COPY --from=builder --chmod=755 --chown=${UID}:${GID} /out/openlist ./
COPY --chmod=755 --chown=${UID}:${GID} entrypoint.sh /entrypoint.sh

USER ${USER}
RUN /entrypoint.sh version

ENV UMASK=022 RUN_ARIA2=${INSTALL_ARIA2}
VOLUME /opt/openlist/data/
EXPOSE 5244 5245
CMD [ "/entrypoint.sh" ]
