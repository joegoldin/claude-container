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

  entrypoint = pkgs.writeShellScript "proxy-entrypoint.sh" ''
    set -e
    PROXY_PROFILE=''${PROXY_PROFILE:-default}
    exec ${python}/bin/python -m claude_proxy.app \
      --profile "$PROXY_PROFILE" \
      --config-dir /config \
      --proxy-port 8080 \
      --dashboard-port 8081
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
  ];

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
