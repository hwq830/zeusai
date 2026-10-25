# Server images, built from the repo root:
#   docker build -f deploy/docker/Dockerfile --target server       -t zeus-server .
#   docker build -f deploy/docker/Dockerfile --target orchestrator -t zeus-orchestrator .
# `server` holds api, admin and zeusctl on distroless; `orchestrator` adds Chromium and CJK fonts
# for PDF export. Prompts, templates, migrations and the report template are embedded in the
# binaries, so the images need no repo files. Base images are build args so a mirror registry
# (ACR, or a Docker Hub mirror in mainland China) can stand in.
ARG GO_IMAGE=golang:1.25-bookworm
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot
ARG CHROMIUM_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/ \
      ./server/cmd/api ./server/cmd/admin ./server/cmd/orchestrator ./server/cmd/zeusctl

FROM ${RUNTIME_IMAGE} AS server
COPY --from=build /out/api /out/admin /out/zeusctl /usr/local/bin/
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/api"]

FROM ${CHROMIUM_IMAGE} AS orchestrator
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium fonts-noto-cjk ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 65532 --create-home zeus
COPY --from=build /out/orchestrator /out/zeusctl /usr/local/bin/
ENV ZEUS_CHROMIUM_PATH=/usr/bin/chromium
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/orchestrator"]
