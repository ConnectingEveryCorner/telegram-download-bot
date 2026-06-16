# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine

ARG VERSION="dev"
ARG COMMIT="unknown"
ARG COMMIT_DATE="unknown"
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

COPY go.mod go.sum go.work go.work.sum ./
COPY core ./core
COPY extension ./extension
COPY app ./app
COPY cmd ./cmd
COPY pkg ./pkg
COPY main.go ./

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 GOMAXPROCS=2 \
    go build -p=1 -trimpath \
    -ldflags "-s -w \
    -X github.com/iyear/tdl/pkg/consts.Version=${VERSION} \
    -X github.com/iyear/tdl/pkg/consts.Commit=${COMMIT} \
    -X github.com/iyear/tdl/pkg/consts.CommitDate=${COMMIT_DATE}" \
    -o /usr/local/bin/telegram-download-bot .

WORKDIR /app
ENV HOME=/data

VOLUME ["/data"]

ENTRYPOINT ["telegram-download-bot"]
