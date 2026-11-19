FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/BardiaYaghmaie/bardiup/internal/version.Version=${VERSION}" \
    -o /bardiup ./cmd/bardiup

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /bardiup /bardiup
USER 65532:65532
ENTRYPOINT ["/bardiup"]
