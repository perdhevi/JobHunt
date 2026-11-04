# Builds the React app, embeds it in a static Go binary, and runs it unprivileged.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# go.mod sets the lowest Go patch release with known fixes. This image won't
# download a newer Go itself, so build with --pull when go.mod moves ahead.
FROM golang:1.27-alpine AS app
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /jobhunt ./cmd/jobhunt

FROM alpine:3.22
# /run/jobhunt holds the board's socket on a VPS (deploy/compose.yaml), and
# only this user may enter it.
RUN adduser -D -u 1000 jobhunt && install -d -o jobhunt /config /data && install -d -o jobhunt -m 700 /run/jobhunt
COPY --from=app /jobhunt /usr/local/bin/jobhunt
USER jobhunt
ENV JOBHUNT_CONFIG=/config/config.json JOBHUNT_LISTEN=0.0.0.0:8787
VOLUME /config
EXPOSE 8787 8788
# Asks the server through its board listener, a port or a socket, whether the
# database answers. Needs no wget or shell in the image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["jobhunt", "health"]
ENTRYPOINT ["jobhunt"]
CMD ["serve"]
