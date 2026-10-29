FROM golang:1.24 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .
COPY access_control.go .
COPY static ./static

RUN CGO_ENABLED=0 \
    GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /jobs-dashboard .

FROM gcr.io/distroless/static-debian13:nonroot

WORKDIR /app
USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /jobs-dashboard /app/jobs-dashboard

EXPOSE 8080

ENTRYPOINT ["/app/jobs-dashboard"]
