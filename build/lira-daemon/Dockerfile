# Build the manager binary
FROM harborxy.msxfcloud.test/library/golang:1.20 as builder

WORKDIR /workspace

COPY . /workspace
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer

# Build
RUN CGO_ENABLED=0 go build -o bin/lira-daemon cmd/lira-daemon/main.go


# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM harborxy.msxfcloud.test/base/base:7.6.3
WORKDIR /
COPY --from=builder /workspace/bin/lira-daemon .
RUN chmod +x /lira-daemon

USER 65532:65532

ENTRYPOINT ["/lira-daemon"]
