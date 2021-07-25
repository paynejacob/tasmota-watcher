FROM golang:1.16-alpine as build

WORKDIR /build

ADD . .

RUN go build -o tasmota-watcher main.go
RUN chmod +x tasmota-watcher

FROM alpine

COPY --from=build /build/tasmota-watcher /usr/local/bin/

ENTRYPOINT ["tasmota-watcher"]