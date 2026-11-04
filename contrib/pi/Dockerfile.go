# Pi worker image with Go, for repositories whose agents build and test Go.
# Build the base image first (see Dockerfile, or Settings, Pi agent), then:
#   docker build -t spine-pi-go:1.5.0 -f contrib/pi/Dockerfile.go contrib/pi
# and set the adapter's container image to spine-pi-go:1.5.0.
ARG PI_VERSION=1.5.0
FROM spine-pi:${PI_VERSION}
ARG GO_VERSION=1.27.3
# From https://go.dev/dl/?mode=json for linux-amd64; change it with GO_VERSION.
ARG GO_SHA256=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
# gcc is for CGO (tree-sitter). curl is only for the download.
RUN apt-get update \
 && apt-get install -y --no-install-recommends curl gcc libc6-dev \
 && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
 && echo "${GO_SHA256}  /tmp/go.tgz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz \
 && apt-get purge -y --auto-remove curl \
 && rm -rf /var/lib/apt/lists/*
# Spine passes the host's PATH into the container, so link into a directory
# every PATH has rather than relying on ENV PATH.
RUN ln -s /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/bin/ \
 && go version | grep -q "go${GO_VERSION} "
# Never let an agent's build fetch a different toolchain.
ENV GOTOOLCHAIN=local
