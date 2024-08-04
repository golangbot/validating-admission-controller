FROM golang:1.22-alpine AS builder
COPY . /src/admissioncontroller/
WORKDIR /src/admissioncontroller
RUN go build -o server

FROM alpine:3.19
WORKDIR /bin/admissioncontroller
COPY --from=builder /src/admissioncontroller/server /bin/admissioncontroller/server
CMD ["./server"]
