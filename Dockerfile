# Image is built by GoReleaser. The binary is cross-compiled outside this
# Dockerfile and made available in the build context as `fngr`.
FROM gcr.io/distroless/static-debian13

COPY fngr /fngr

ENTRYPOINT ["/fngr"]
