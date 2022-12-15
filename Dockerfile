FROM golang:1.19-bullseye

RUN apt-get update && apt-get install -y libusb-1.0-0 libusb-dev

WORKDIR /usr/src/app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN go build -v -o usbredir-hook

FROM registry.access.redhat.com/ubi9/ubi-micro:latest

COPY --from=build /usr/src/app/usbredir-hook /usbredir-hook

WORKDIR /
ENTRYPOINT ["/usbredir-hook"]
