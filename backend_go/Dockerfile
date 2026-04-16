FROM golang:1.24-alpine AS build
RUN apk add --no-cache git ca-certificates && update-ca-certificates
WORKDIR /app
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /towel-backend .

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/rajivpatki/YOUR_REPO"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /towel-backend /usr/local/bin/towel-backend
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/towel-backend"]
