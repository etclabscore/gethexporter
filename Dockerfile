FROM golang:1.14-alpine as base
RUN apk add --no-cache libstdc++ gcc g++ make git ca-certificates linux-headers
MAINTAINER "Meows D. Bits (https://github.com/meowsbits)"
WORKDIR /go/src/github.com/etclabscore/gethexporter
ADD . .
RUN go get && go install

FROM alpine:latest
MAINTAINER "Meows D. Bits (https://github.com/etclabscore/meowsbits)"
RUN apk add --no-cache jq ca-certificates linux-headers
COPY --from=base /go/bin/gethexporter /usr/local/bin/gethexporter
ENV GETH http://127.0.0.1:8545
ENV ADDRESSES ""
ENV DELAY 1000

EXPOSE 6061
ENTRYPOINT gethexporter
