"""Entry point that runs both mitmproxy and the web dashboard in a single process.

Starts the mitmproxy DumpMaster with the ProxyAddon for request interception,
and a Starlette/Uvicorn web dashboard in a background thread for real-time
rule management.
"""

import argparse
import asyncio
import logging
import os
import threading

import uvicorn
from mitmproxy import options
from mitmproxy.tools.dump import DumpMaster

from claude_proxy.addon import ProxyAddon
from claude_proxy.dashboard import app, configure, on_pending_request, set_dashboard_loop
from claude_proxy.rules import RuleStore

logger = logging.getLogger(__name__)


def parse_args() -> argparse.Namespace:
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(description="Claude HTTP/HTTPS Proxy")
    parser.add_argument(
        "--profile",
        default="default",
        help="Profile name for rule persistence (default: default)",
    )
    parser.add_argument(
        "--config-dir",
        default="/config",
        help="Base configuration directory (default: /config)",
    )
    parser.add_argument(
        "--proxy-port",
        type=int,
        default=8080,
        help="Port for the mitmproxy listener (default: 8080)",
    )
    parser.add_argument(
        "--dashboard-port",
        type=int,
        default=8081,
        help="Port for the web dashboard (default: 8081)",
    )
    parser.add_argument(
        "--hold-timeout",
        type=float,
        default=120,
        help="Seconds before pending requests are auto-denied (default: 120)",
    )
    return parser.parse_args()


def _start_dashboard(port: int) -> None:
    """Run the Starlette dashboard via uvicorn in a daemon thread."""
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    set_dashboard_loop(loop)

    config = uvicorn.Config(
        app,
        host="0.0.0.0",
        port=port,
        log_level="info",
        access_log=False,
        loop="none",  # Use the loop we already created
    )
    server = uvicorn.Server(config)
    server.run()


async def run_proxy(args: argparse.Namespace) -> None:
    """Set up and run the proxy and dashboard."""
    # 1. Create profile directory
    profile_dir = os.path.join(args.config_dir, "profiles")
    os.makedirs(profile_dir, exist_ok=True)
    profile_path = os.path.join(profile_dir, f"{args.profile}.json")

    # 2. Load or create RuleStore
    store = RuleStore()
    if os.path.exists(profile_path):
        try:
            store.load(profile_path)
            logger.info("Loaded rules from %s", profile_path)
        except Exception:
            logger.exception("Failed to load rules from %s, starting fresh", profile_path)

    # 3. Create ProxyAddon
    addon = ProxyAddon(
        rule_store=store,
        on_pending=on_pending_request,
        hold_timeout=args.hold_timeout,
    )

    # 4. Configure dashboard with dependencies
    configure(addon, store, profile_path)

    # 5. Start dashboard in background daemon thread
    dashboard_thread = threading.Thread(
        target=_start_dashboard,
        args=(args.dashboard_port,),
        daemon=True,
    )
    dashboard_thread.start()
    logger.info("Dashboard started on port %d", args.dashboard_port)

    # 6. Set up mitmproxy CA cert directory
    ca_dir = os.path.join(args.config_dir, "ca")
    os.makedirs(ca_dir, exist_ok=True)

    # 7. Create mitmproxy DumpMaster
    opts = options.Options(
        listen_host="0.0.0.0",
        listen_port=args.proxy_port,
        confdir=ca_dir,
        ssl_insecure=True,
    )
    master = DumpMaster(opts)
    master.addons.add(addon)
    logger.info("Proxy listening on port %d", args.proxy_port)

    # 8. Start periodic cleanup loop
    async def cleanup_loop() -> None:
        while True:
            await asyncio.sleep(10)
            try:
                timed_out = addon.cleanup_timed_out()
                if timed_out:
                    logger.info("Timed out %d pending flows", len(timed_out))
                store.cleanup_expired()
            except Exception:
                logger.exception("Error during cleanup")

    cleanup_task = asyncio.create_task(cleanup_loop())

    # 9. Run mitmproxy master
    try:
        await master.run()
    finally:
        cleanup_task.cancel()
        try:
            await cleanup_task
        except asyncio.CancelledError:
            pass


def main() -> None:
    """Main entry point."""
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    )
    args = parse_args()
    asyncio.run(run_proxy(args))


if __name__ == "__main__":
    main()
