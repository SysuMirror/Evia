#!/usr/bin/env python3
"""Loopback-only TCP failover proxy for the ClickHouse HA drill.

The proxy fronts several ClickHouse HTTP nodes behind one stable endpoint so
the Evia application keeps its single-URL configuration while a node is
stopped. It is intentionally small and dependency-free:

* ``BackendSelector`` is pure state-machine logic: passive dial failures mark
  a backend down after a closed threshold, periodic active probes bring it
  back after a closed success threshold, and ``pick`` always prefers the first
  healthy backend in configuration order so routing stays deterministic.
* The asyncio layer only performs TCP forwarding and feeds dial outcomes into
  the selector; no HTTP parsing happens here, so the proxy is protocol-neutral
  and cannot alter application payloads.

This is a drill utility, not a production load balancer: there is no TLS, no
authentication and no connection queuing. Production deployments should use
chproxy, HAProxy or an equivalent managed L4/L7 tier.
"""

import argparse
import asyncio
import os
from dataclasses import dataclass, field


@dataclass
class Backend:
    """One upstream TCP endpoint and its current failover state."""

    host: str
    port: int
    healthy: bool = True
    consecutive_failures: int = 0
    consecutive_successes: int = 0

    @property
    def address(self):
        return f"{self.host}:{self.port}"


class BackendSelector:
    """Deterministic ordered failover with bounded health transitions."""

    def __init__(self, backends, unhealthy_threshold=2, healthy_threshold=2):
        if not backends:
            raise ValueError("at least one backend is required")
        if unhealthy_threshold < 1 or healthy_threshold < 1:
            raise ValueError("health thresholds must be positive")
        self.backends = list(backends)
        self.unhealthy_threshold = unhealthy_threshold
        self.healthy_threshold = healthy_threshold

    @classmethod
    def from_specs(cls, specs, **kwargs):
        backends = []
        for spec in specs:
            host, _, port = spec.rpartition(":")
            if not host or not port.isdigit() or not 1 <= int(port) <= 65535:
                raise ValueError(f"invalid backend specification: {spec}")
            backends.append(Backend(host=host, port=int(port)))
        return cls(backends, **kwargs)

    def pick(self):
        """Return the first healthy backend, or None when everything is down."""
        return next((backend for backend in self.backends if backend.healthy), None)

    def record_failure(self, backend):
        backend.consecutive_failures += 1
        backend.consecutive_successes = 0
        if backend.healthy and backend.consecutive_failures >= self.unhealthy_threshold:
            backend.healthy = False

    def record_success(self, backend):
        backend.consecutive_successes += 1
        backend.consecutive_failures = 0
        if not backend.healthy and backend.consecutive_successes >= self.healthy_threshold:
            backend.healthy = True

    def healthy_addresses(self):
        return [backend.address for backend in self.backends if backend.healthy]


class TCPFailoverProxy:
    def __init__(self, selector, probe_interval=2.0, dial_timeout=2.0):
        self.selector = selector
        self.probe_interval = probe_interval
        self.dial_timeout = dial_timeout
        self.server = None

    async def _dial_backend(self, backend):
        try:
            reader, writer = await asyncio.wait_for(
                asyncio.open_connection(backend.host, backend.port), timeout=self.dial_timeout
            )
            return reader, writer
        except (OSError, asyncio.TimeoutError):
            return None, None

    async def _pipe(self, reader, writer):
        try:
            while True:
                data = await reader.read(65536)
                if not data:
                    break
                writer.write(data)
                await writer.drain()
        except (OSError, asyncio.CancelledError):
            pass
        finally:
            can_close = writer.can_write_eof()
            if can_close:
                try:
                    writer.write_eof()
                except OSError:
                    pass
            writer.close()

    async def handle_client(self, client_reader, client_writer):
        backend = self.selector.pick()
        upstream_writer = None
        try:
            while backend is not None:
                upstream_reader, upstream_writer = await self._dial_backend(backend)
                if upstream_writer is not None:
                    self.selector.record_success(backend)
                    break
                self.selector.record_failure(backend)
                backend = self.selector.pick()
            if upstream_writer is None:
                # Every backend is unavailable: refuse so the application
                # observes a connection error instead of a silent hang.
                client_writer.close()
                return
            await asyncio.gather(
                self._pipe(client_reader, upstream_writer),
                self._pipe(upstream_reader, client_writer),
            )
        finally:
            client_writer.close()
            if upstream_writer is not None:
                upstream_writer.close()

    async def _health_probe(self):
        while True:
            await asyncio.sleep(self.probe_interval)
            for backend in self.selector.backends:
                _, writer = await self._dial_backend(backend)
                if writer is None:
                    self.selector.record_failure(backend)
                else:
                    self.selector.record_success(backend)
                    writer.close()

    async def serve(self, host, port):
        self.server = await asyncio.start_server(self.handle_client, host, port)
        probe_task = asyncio.create_task(self._health_probe())
        try:
            await self.server.serve_forever()
        finally:
            probe_task.cancel()
            self.server.close()
            await self.server.wait_closed()


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen-host", default=os.environ.get("EVIA_CH_PROXY_HOST", "0.0.0.0"))
    parser.add_argument("--listen-port", type=int, default=int(os.environ.get("EVIA_CH_PROXY_PORT", "8123")))
    parser.add_argument(
        "--backends",
        default=os.environ.get("EVIA_CH_PROXY_BACKENDS", ""),
        help="comma-separated host:port upstreams, e.g. 172.31.79.10:8123,172.31.79.11:8123",
    )
    parser.add_argument("--probe-interval", type=float, default=2.0)
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    specs = [item.strip() for item in args.backends.split(",") if item.strip()]
    selector = BackendSelector.from_specs(specs)
    proxy = TCPFailoverProxy(selector, probe_interval=args.probe_interval)
    asyncio.run(proxy.serve(args.listen_host, args.listen_port))


if __name__ == "__main__":
    main()
