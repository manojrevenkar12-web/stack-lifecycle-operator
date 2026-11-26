# Operator image: static binary on distroless, non-root, no shell.
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd/operator

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/manager /manager
# no fixed UID beyond distroless' nonroot default; OpenShift replaces it with one from the namespace range
USER 65532:65532
ENTRYPOINT ["/manager"]
