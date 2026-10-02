FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /rcon-exporter .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /rcon-exporter /rcon-exporter
ENTRYPOINT ["/rcon-exporter"]
