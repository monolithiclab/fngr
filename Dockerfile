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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

COPY fngr /fngr

ENTRYPOINT ["/fngr"]
