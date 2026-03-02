FROM golang:1.24 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
COPY . .
RUN go mod tidy
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o /panel-core ./cmd/server/

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /panel-core .
USER 65532:65532
ENTRYPOINT ["/panel-core"]
