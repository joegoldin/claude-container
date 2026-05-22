# nix/proxy-image.nix
{ pkgs }:
let
  python = pkgs.python3.withPackages (
    ps: with ps; [
      mitmproxy
      starlette
      uvicorn
      websockets
    ]
  );

  proxySource = ../proxy;

  # Dedicated uid for the mitmproxy process so the firewall rules in the
  # shared network namespace can exempt its outbound traffic via
  # `meta skuid` matching. Any other uid in the netns (i.e. the Claude
  # container's processes) is force-redirected through mitmproxy.
  proxyUid = "1500";
  proxyGid = "1500";

  entrypoint = pkgs.writeShellScript "proxy-entrypoint.sh" ''
    set -e
    PROXY_SESSION=''${PROXY_SESSION:-default}

    # Make sure /config (host bind mount) is writable by the mitmproxy uid.
    # The proxy needs to write the CA dir, the dashboard token, and the
    # rules profile JSON. The host owner of this dir is the user who ran
    # `claude-container`; after this chown the host sees uid 1500 on these
    # internal files, which is fine — they're managed state.
    ${pkgs.coreutils}/bin/chown -R ${proxyUid}:${proxyGid} /config 2>/dev/null || true

    # ----- Default-deny firewall in the shared network namespace -----
    #
    # The Claude container joins this namespace via `--network container:`,
    # so installing rules here locks down ALL traffic for both containers.
    #
    # Strategy:
    #   - filter OUTPUT default DROP, allowlist what we explicitly need.
    #   - nat OUTPUT REDIRECTs every non-loopback TCP connection from any
    #     uid OTHER THAN mitmproxy (1500) to mitmproxy's transparent
    #     listener on port 8080. mitmproxy reads SO_ORIGINAL_DST to learn
    #     the real destination, then either MITMs HTTPS/HTTP or proxies
    #     raw TCP through to upstream.
    #   - UDP/443 (QUIC/HTTP3) is dropped outright so clients fall back
    #     to TCP, where the redirect catches them.
    #   - DNS is allowed unrestricted for now (DoH over 443 still goes
    #     through mitmproxy because of the TCP redirect).
    ${pkgs.nftables}/bin/nft -f - <<NFT
    table inet claude_proxy_fw {
      chain output {
        type filter hook output priority 0; policy drop;

        # Loopback (dashboard, mitmproxy local listener, embedded DNS).
        oif "lo" accept

        # Established/related return traffic.
        ct state established,related accept

        # mitmproxy itself can talk to anything upstream.
        meta skuid ${proxyUid} accept

        # Local listeners that other processes in the netns may connect to.
        tcp dport 8080 accept
        tcp dport 8081 accept

        # DNS to docker's embedded resolver (127.0.0.11) is allowed via the
        # oif "lo" rule above. EXTERNAL DNS over UDP/53 is denied so a
        # confused agent cannot exfiltrate via crafted subdomain queries
        # (GAP-1 from docs/security/audit-2026-05-22.md). DNS-over-TCP and
        # DNS-over-HTTPS still pass — but for those, the proxy REDIRECT
        # catches them and applies the rule store.
        #
        # If a workload genuinely needs external UDP DNS (rare — most
        # callers go through libc's resolver, which hits the docker
        # embedded resolver), set CLAUDE_PROXY_ALLOW_DNS_UDP=1 in the
        # proxy image's env to opt back in.
        # NB: 127.0.0.11 traffic stays on the lo interface, so the explicit
        # `oif "lo" accept` rule already covers it.
        tcp dport 53 accept

        # Kill QUIC so HTTPS clients downgrade to TCP and hit the redirect.
        udp dport 443 drop

        # Kill external UDP/53 explicitly so the drop is visible in the log
        # prefix below (not just a silent "default policy drop").
        udp dport 53 log prefix "claude_proxy_fw dns-udp drop: " level debug drop

        # Everything else: drop. Logged so we can see what's being blocked
        # during smoke tests; remove the log statement later if noisy.
        log prefix "claude_proxy_fw drop: " level debug
      }

      chain input {
        type filter hook input priority 0; policy drop;
        iif "lo" accept
        ct state established,related accept
        # Dashboard port-forwarded from the host.
        tcp dport 8081 accept
        # Transparent listener (only reached via the local REDIRECT, but
        # accept defensively in case Docker's portmap touches it).
        tcp dport 8080 accept
      }

      chain prerouting_nat {
        type nat hook prerouting priority -100;
      }

      chain output_nat {
        type nat hook output priority -100;
        # Skip mitmproxy's own outbound (would loop forever).
        meta skuid ${proxyUid} return
        # Skip loopback so dashboard / embedded DNS work.
        oif "lo" return
        # Redirect ALL other TCP to mitmproxy transparent listener.
        # SO_ORIGINAL_DST gives mitmproxy the real destination.
        meta l4proto tcp redirect to :8080
      }
    }
    NFT

    # ----- Run mitmproxy as the dedicated uid -----
    exec ${pkgs.su-exec}/bin/su-exec ${proxyUid}:${proxyGid} \
      ${python}/bin/python -m claude_proxy.app \
        --session "$PROXY_SESSION" \
        --config-dir /config \
        --proxy-port 8080 \
        --dashboard-port 8081 \
        --transparent
  '';
in
pkgs.dockerTools.buildLayeredImage {
  name = "claude-proxy";
  tag = "latest";

  contents = [
    python
    pkgs.bash
    pkgs.coreutils
    pkgs.cacert
    pkgs.curl
    pkgs.nftables
    pkgs.iproute2
    pkgs.su-exec
    pkgs.shadow
  ];

  enableFakechroot = true;

  fakeRootCommands = ''
    ${pkgs.dockerTools.shadowSetup}
    groupadd -g ${proxyGid} mitmproxy
    useradd -u ${proxyUid} -g ${proxyGid} -d /var/empty -s ${pkgs.bash}/bin/bash -M mitmproxy
  '';

  extraCommands = ''
    mkdir -p config opt
    # Copy proxy source into image
    cp -r ${proxySource}/claude_proxy opt/claude_proxy
    cp -r ${proxySource}/static opt/static
  '';

  config = {
    Entrypoint = [ "${entrypoint}" ];
    ExposedPorts = {
      "8080/tcp" = { };
      "8081/tcp" = { };
    };
    Env = [
      "PYTHONPATH=/opt"
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
    ];
  };
}
