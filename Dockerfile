# Pin the builder to the build host's platform ($BUILDPLATFORM) so Go
# cross-compiles to the target platform via GOOS/GOARCH below, instead of
# running the toolchain under QEMU emulation for non-native target
# architectures during a multiarch build.
FROM --platform=$BUILDPLATFORM golang:1.27 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter).
COPY . .

# Build the whole ./cmd package rather than a single file, so additional files
# in that package are picked up.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
