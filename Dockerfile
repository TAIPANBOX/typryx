# The optional add-on as a published image.
#
# Static, distroless, non-root. CGO off is what makes the binary runnable on
# distroless static at all: there is no libc on the final image for a
# dynamically linked binary to find.
#
# NEEDS BUILDKIT for --platform=$BUILDPLATFORM. BuildKit is the default in
# Docker 23+ and in Docker Desktop; a host without it needs
# `docker buildx build`, or drop the `--platform=` line below and lose only
# the cross-compile.
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
# Dependencies first, so a code-only change does not re-download the module
# graph on every build.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/typryx ./cmd/typryx

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="typryx"
LABEL org.opencontainers.image.description="An optional add-on: answers a typed question (choice, score, or yes/no) with a probability, over HTTP and MCP."
LABEL org.opencontainers.image.source="https://github.com/TAIPANBOX/typryx"
LABEL org.opencontainers.image.licenses="Apache-2.0"

# answers.ndjson and outcomes.ndjson live here when TYPRYX_LEDGER_DIR names
# this path. Mounted and never baked, so the record outlives the container.
VOLUME ["/var/lib/typryx"]

COPY --from=build /out/typryx /usr/local/bin/typryx
COPY examples/templates /etc/typryx/templates

# 65532 is distroless's `nonroot` uid. Numeric on purpose: a kubelet with
# runAsNonRoot cannot verify a NAME and refuses the container outright.
USER 65532:65532

EXPOSE 4320

# The default build's own default (TYPRYX_ADDR=127.0.0.1:4320, see
# cmd/typryx/main.go's defaultAddr) is loopback, which inside a container
# reaches nothing from outside it: a container needs a bind an operator can
# actually reach. Binding wide here is therefore a deliberate departure from
# the binary's own default, made only for the image, and it lands squarely
# in the open-bind refusal matrix (README's "Configuration" and CLAUDE.md
# invariant 4): with neither TYPRYX_KEYS nor TYPRYX_ALLOW_OPEN_BIND=1 set,
# this refuses to start rather than open an unauthenticated door. That
# refusal is the matrix working as designed, not a defect in the image.
ENV TYPRYX_ADDR=0.0.0.0:4320
ENV TYPRYX_TEMPLATES=/etc/typryx/templates

ENTRYPOINT ["/usr/local/bin/typryx"]
