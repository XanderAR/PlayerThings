FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/playradar ./cmd/playradar

# Alpine rather than distroless so `railway ssh` can run `/app/playradar stats`.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/playradar /app/playradar
COPY data/playtests.json data/collections.json data/moments.json /app/data/
# Mount a Railway volume at /data so the database survives deploys.
ENV DATABASE_PATH=/data/playradar.db
EXPOSE 8080
ENTRYPOINT ["/app/playradar"]
