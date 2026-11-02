# Builds the React app, embeds it in a static Go binary, and runs it unprivileged.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

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
RUN adduser -D -u 1000 jobhunt && install -d -o jobhunt /config
COPY --from=app /jobhunt /usr/local/bin/jobhunt
USER jobhunt
ENV JOBHUNT_CONFIG=/config/config.json JOBHUNT_LISTEN=0.0.0.0:8787
VOLUME /config
EXPOSE 8787 8788
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD wget -q -O /dev/null http://127.0.0.1:8787/api/settings
ENTRYPOINT ["jobhunt"]
CMD ["serve"]
