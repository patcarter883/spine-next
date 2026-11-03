# Pi worker image for Spine (adapter protocol: pi-rpc).
# Pin the version: Pi ships breaking changes in minor releases, and Spine
# refuses to run a version other than the adapter's `version`.
#   docker build -t spine-pi:1.5.0 --build-arg PI_VERSION=1.5.0 contrib/pi
ARG NODE_IMAGE=node:22-bookworm-slim
FROM ${NODE_IMAGE}
ARG PI_VERSION=1.5.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates git ripgrep \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g --ignore-scripts @earendil-works/pi-coding-agent@${PI_VERSION} \
 && pi --version | grep -qx "${PI_VERSION}"
# Spine runs the container as your uid with HOME=/tmp/spine-home and mounts
# only the task worktree (rw), its git metadata and a private Pi agent dir.
