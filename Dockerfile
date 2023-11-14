# syntax=docker/dockerfile:1

FROM golang:1.21.3-bookworm

# Set destination for COPY
WORKDIR /app

COPY . .
RUN go mod download

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o /proxysql-agent

# Run
CMD ["/proxysql-agent"]
