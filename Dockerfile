FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /fizzbuzz .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /fizzbuzz /fizzbuzz
EXPOSE 8080
ENTRYPOINT ["/fizzbuzz"]
