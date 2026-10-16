FROM node:22-bookworm-slim AS excalidraw

RUN npm install --prefix /opt/laxcode-excalidraw --omit=dev --no-audit --no-fund \
        mcp-excalidraw-server@2.1.2

FROM golang:1.26.6-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash python3 ripgrep jq fontconfig fonts-noto-cjk \
    && jq --version \
    && rm -rf /var/lib/apt/lists/*

COPY --from=excalidraw /usr/local/bin/node /usr/local/bin/node
COPY --from=excalidraw /opt/laxcode-excalidraw /opt/laxcode-excalidraw
RUN ln -s /opt/laxcode-excalidraw/node_modules/mcp-excalidraw-server/dist/bin.js \
        /usr/local/bin/excalidraw-canvas \
    && node --version \
    && excalidraw-canvas --version

ENV GOTOOLCHAIN=local GOPROXY=off PIP_NO_INDEX=1 HOME=/tmp/agenthome
USER 1000:1000
ENTRYPOINT []
CMD ["/bin/bash"]
