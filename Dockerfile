FROM golang:1.24 AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/limen ./cmd/limen

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/limen /limen
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD ["/limen", "healthcheck"]
ENTRYPOINT ["/limen"]
