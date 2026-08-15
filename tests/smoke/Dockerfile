# Nix installation/update smoke environment. The base is multi-architecture
# and pinned by digest; machtiani itself is never preinstalled in the image.
FROM nixos/nix:2.24.5@sha256:4ad79a0ab633944869a37921f096d35a3f2c7a0275d98b7bfa0cd3cba5a6b96e

ENV NIX_CONFIG="experimental-features = nix-command flakes"

COPY . /fixtures/mct-source
WORKDIR /workspace

# Realize the locked smoke toolchain once at image-build time. The runtime
# still performs the real install and update builds from its local Git remote.
RUN nix develop path:/fixtures/mct-source#smoke -c true

CMD ["nix", "develop", "path:/fixtures/mct-source#smoke", "-c", "bash", "/fixtures/mct-source/tests/smoke/container.sh"]
