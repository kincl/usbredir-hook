FROM golang:1.19-bullseye as build

RUN apt-get update && apt-get install -y libusb-1.0-0 libusb-1.0-0-dev libusb-dev

WORKDIR /usr/src/app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN go build -o usbredir-hook && \
    go install github.com/google/gousb/lsusb@latest

FROM registry.access.redhat.com/ubi9/ubi:latest

RUN dnf -y install http://mirror.stream.centos.org/9-stream/BaseOS/x86_64/os/Packages/libusbx-1.0.26-1.el9.x86_64.rpm

COPY --from=build /usr/src/app/usbredir-hook /usbredir-hook
COPY --from=build /go/bin/lsusb /bin/lsusb

WORKDIR /
ENTRYPOINT ["/usbredir-hook"]
