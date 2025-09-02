# Use a base Golang image
FROM golang:1.23-alpine

# Install necessary packages for protoc
RUN apk add --no-cache unzip protobuf-dev protobuf

# Install protoc-gen-go and protoc-gen-go-grpc
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

