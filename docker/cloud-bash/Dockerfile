FROM golang:1.26.6-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash python3 ripgrep \
    && rm -rf /var/lib/apt/lists/*

ENV GOTOOLCHAIN=local GOPROXY=off PIP_NO_INDEX=1 HOME=/tmp/agenthome
USER 1000:1000
ENTRYPOINT []
CMD ["/bin/bash"]
