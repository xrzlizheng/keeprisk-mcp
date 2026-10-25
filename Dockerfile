FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /keeprisk-mcp ./cmd/keeprisk-mcp

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /keeprisk-mcp /keeprisk-mcp
ENV MCP_HTTP_ADDR=:8080
ENV MCP_HTTP_PATH=/mcp
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/keeprisk-mcp"]