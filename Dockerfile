FROM golang:1.23-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/medgit ./cmd/medgit

FROM debian:bookworm-slim

RUN mkdir -p /data && chown nobody:nogroup /data
COPY --from=build /out/medgit /usr/local/bin/medgit
ENV MEDGIT_DATA=/data \
    PORT=8080
EXPOSE 8080
USER nobody
ENTRYPOINT ["/usr/local/bin/medgit"]
CMD ["serve"]
