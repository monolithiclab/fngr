# Image is built by GoReleaser. The binary is cross-compiled outside this
# Dockerfile and made available in the build context as `fngr`.
#
# `nonroot` (UID 65532), so a `docker run` that forgets `--user` is not root
# on the volume the user mounted. The cost is that the database has to be
# bind-mounted as a directory rather than a single file; README's container
# section documents that form and why.
#
# Pinned by digest, because the tag moves; the tag is kept beside it for
# readability only. docs/PUBLISHING.md has the refresh recipe.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

COPY fngr /fngr

ENTRYPOINT ["/fngr"]
