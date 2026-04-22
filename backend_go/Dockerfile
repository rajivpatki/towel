FROM golang:1.26-alpine AS build
RUN apk add --no-cache build-base ca-certificates && update-ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-w -s" -o /towel-backend .

FROM alpine:3.22
LABEL org.opencontainers.image.source="https://github.com/rajivpatki/YOUR_REPO"
RUN apk add --no-cache ca-certificates
COPY --from=build /towel-backend /usr/local/bin/towel-backend
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/towel-backend"]
