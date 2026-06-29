FROM golang:1.25.7-alpine
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /usr/local/bin/safegit .
