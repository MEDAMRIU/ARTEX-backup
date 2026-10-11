#
# 运行镜像（不在镜像里编译）：只装常用工具，放入**预编译好的 Linux 单二进制**。
# 二进制由 CI 的 binaries job 交叉编译（纯 Go、无 QEMU），按目标架构放在
# 构建上下文的 dist/<TARGETARCH>/artex。这样多架构构建时 arm64 只需模拟 apt 层，
# 不再模拟 Next/Go 编译，速度快得多。
#
# 本地手动构建镜像时，先自行准备二进制：
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392 AS node
FROM python:3.12-slim-bookworm@sha256:34386ef0cb081344d7ec1c103ba398e6e9f64e9ab3a1509accc92a4e24a07258
ARG TARGETARCH
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && rm -rf /var/lib/apt/lists/*
# Install exact tools and integrity-locked transitive dependencies at build time.
COPY runtime-tools/package*.json /opt/artex-tools/
ENV PATH="/opt/artex-tools/node_modules/.bin:${PATH}" \
    PLAYWRIGHT_BROWSERS_PATH=/opt/playwright \
    NORMA_RIPGREP_NO_INSTALL=1
RUN cd /opt/artex-tools && npm ci --omit=dev --ignore-scripts \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* /root/.npm
# Keep legacy npx invocations resolving to the same locked local packages.
RUN mkdir -p /usr/local/lib/node_modules/@playwright \
    && ln -s /opt/artex-tools/node_modules/@playwright/mcp /usr/local/lib/node_modules/@playwright/mcp \
    && ln -s /opt/artex-tools/node_modules/@playwright/cli /usr/local/lib/node_modules/@playwright/cli \
    && ln -s /opt/artex-tools/node_modules/playwright /usr/local/lib/node_modules/playwright
RUN groupadd --gid 10001 artex && useradd --uid 10001 --gid artex --create-home artex
WORKDIR /app
COPY runtime-tools/package*.json /app/
RUN ln -s /opt/artex-tools/node_modules /app/node_modules
# 预编译好的对应架构二进制（dist/amd64/artex 或 dist/arm64/artex）
COPY --chown=artex:artex dist/${TARGETARCH}/artex /app/artex
# 守护启动脚本：进程退出后按退出码决定是否重新拉起，页面一键更新靠它完成换装。
# 它同时负责把 SIGTERM 转发给 artex —— docker stop 只把信号发给 PID 1，
# 不转发的话 artex 收不到、做不了优雅关闭，10 秒后被 SIGKILL 硬杀。
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY --chown=artex:artex skills/ /app/skills/
RUN mkdir -p /app/data /app/auth && chown -R artex:artex /app
USER artex
ENV ARTEX_AUTH_DIR=/app/auth
# data/（SQLite + jwt.key）持久化点
VOLUME ["/app/data", "/app/auth"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
