FROM golang:1.11-alpine as base
RUN apk add --no-cache libstdc++ gcc g++ make git ca-certificates linux-headers
MAINTAINER "Ted Fryer (https://github.com/devfdn)"
WORKDIR /go/src/github.com/devfdn/gethexporter
ADD . .
RUN go get && go install

FROM alpine:latest
MAINTAINER "Ted Fryer (https://github.com/devfdn)"
RUN apk add --no-cache jq ca-certificates linux-headers
COPY --from=base /go/bin/gethexporter /usr/local/bin/gethexporter
ENV GETH http://127.0.0.1:8545
ENV ADDRESSES ""
ENV DELAY 1000

EXPOSE 6061
ENTRYPOINT gethexporter
