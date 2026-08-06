FROM golang:1.26 AS build
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
ENTRYPOINT ["/skillsd"]
