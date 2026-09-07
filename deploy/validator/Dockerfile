# A local Solana test validator — this is the sandbox.
#
# Running a real validator rather than pointing at a public test network makes the demo
# self-contained: it works on a fresh VM with no accounts, no faucet queue and no rate limit, and
# every signature and read-back in the demo is genuine.
# Pinned to amd64 deliberately. Agave publishes the validator only for x86_64 Linux, so an arm64
# base would pull an arm64 loader and the x86_64 binary would fail with a rosetta error that does
# not name its own cause. On an x86_64 VM this is native; on an Apple Silicon laptop it runs under
# emulation, which is slower to start and perfectly correct.
FROM --platform=linux/amd64 debian:bookworm-slim

ARG SOLANA_VERSION=v2.1.11
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl bzip2 libssl3 && \
    rm -rf /var/lib/apt/lists/*

RUN curl -sSfL "https://github.com/anza-xyz/agave/releases/download/${SOLANA_VERSION}/solana-release-x86_64-unknown-linux-gnu.tar.bz2" \
      | tar -xj -C /opt && \
    mv /opt/solana-release /opt/solana
ENV PATH=/opt/solana/bin:$PATH

RUN mkdir -p /ledger
EXPOSE 8899 8900
ENTRYPOINT ["solana-test-validator"]
