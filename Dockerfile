FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/virtualbox ./cmd/virtualbox

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/virtualbox /virtualbox
VOLUME /data
EXPOSE 25 587 465
ENTRYPOINT ["/virtualbox"]
CMD ["-config", "/etc/virtualbox/config.json"]
