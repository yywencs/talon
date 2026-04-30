FROM golang:bookworm

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    coreutils \
    curl \
    file \
    findutils \
    gawk \
    git \
    grep \
    less \
    make \
    procps \
    ripgrep \
    sed \
    tmux \
    unzip \
    xz-utils \
    zip \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
