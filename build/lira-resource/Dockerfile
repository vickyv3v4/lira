# Build the manager binary
FROM harborxy.msxfcloud.test/library/golang:1.20 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY . /workspace
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
# Copy the go source

# Build
RUN CGO_ENABLED=0 go build -o bin/lira-resource cmd/lira-resource/main.go


# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM harborxy.msxfcloud.test/base/base:7.6.3
WORKDIR /
COPY --from=builder /workspace/bin/lira-resource .
RUN chmod +x /lira-resource

USER 65532:65532

ENTRYPOINT ["/lira-resource"]
