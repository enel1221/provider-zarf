# Multi-stage build to add kubectl and busybox for debugging
# Stage 1: Download kubectl and busybox
FROM alpine:3.19 AS tools

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.31.4

# Install curl for downloading binaries
RUN apk add --no-cache curl ca-certificates

# Download kubectl for the target architecture
RUN case ${TARGETARCH} in \
    "amd64") KUBECTL_ARCH=amd64 ;; \
    "arm64") KUBECTL_ARCH=arm64 ;; \
    *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -L "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${KUBECTL_ARCH}/kubectl" -o /kubectl && \
    curl -L "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${KUBECTL_ARCH}/kubectl.sha256" -o /kubectl.sha256 && \
    echo "$(cat /kubectl.sha256)  /kubectl" | sha256sum -c - && \
    chmod +x /kubectl

# Use Alpine's busybox and create symlinks in a staging directory
# Alpine busybox is statically linked and works on distroless
RUN mkdir -p /bin-stage && \
    cp /bin/busybox /bin-stage/busybox && \
    /bin-stage/busybox --install -s /bin-stage

# Stage 2: Create minimal runtime image with distroless/base (includes glibc, needed for kubectl)
FROM gcr.io/distroless/base-debian12:latest

ARG TARGETOS
ARG TARGETARCH

# Copy provider binary
ADD bin/$TARGETOS\_$TARGETARCH/provider /usr/local/bin/crossplane-zarf-provider

# Copy kubectl from tools stage
COPY --from=tools /kubectl /usr/local/bin/kubectl

# Copy busybox and all its symlinks
COPY --from=tools /bin-stage/* /bin/

USER 65532
ENTRYPOINT ["crossplane-zarf-provider"]


