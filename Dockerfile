# syntax=docker/dockerfile:1
# Build
FROM --platform=$BUILDPLATFORM docker.io/golang:1.25-alpine AS builder
ARG TARGETOS TARGETARCH
WORKDIR /workspace
COPY . .
# We must run with CGO_ENABLED=0 because otherwise the alpine container wont be able to launch it unless we install more packages
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o chatbot

# Deployment
FROM alpine:latest
WORKDIR /root/
COPY --from=builder /workspace/chatbot ./
EXPOSE 8080
ENTRYPOINT ["./chatbot"]
