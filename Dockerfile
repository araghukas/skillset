FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/skillsd ./cmd/skillsd
RUN CGO_ENABLED=0 go build -o /out/skillsd-registry ./cmd/skillsd-registry
RUN CGO_ENABLED=0 go build -o /out/skillsd-init ./cmd/skillsd-init

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/skillsd /skillsd
COPY --from=build /out/skillsd-registry /skillsd-registry
COPY --from=build /out/skillsd-init /skillsd-init
USER nonroot:nonroot
# skillsd and skillsd-registry default to :8080/:8081 respectively (see
# internal/config, internal/registryconfig); skillsd-init has no listener.
EXPOSE 8080 8081
ENTRYPOINT ["/skillsd"]
