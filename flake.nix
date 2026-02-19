{
  description = "Run multiple Claude Code instances in isolated containers";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        vendorHash = "sha256-R2ZdV4TED0zp/nQUcxo1uvXQxPljlRg5CCCiwN6+3aY=";
      in
      {
        packages.default = self.packages.${system}.claude-container;

        packages.claude-container = pkgs.buildGoModule {
          pname = "claude-container";
          version = "0.1.0";
          src = ./.;
          inherit vendorHash;
          doCheck = false; # tests run via checks.default with proper env

          nativeBuildInputs = with pkgs; [
            installShellFiles
            makeWrapper
          ];

          postInstall = ''
            # Generate shell completions
            $out/bin/claude-container completion bash > claude-container.bash
            $out/bin/claude-container completion fish > claude-container.fish
            $out/bin/claude-container completion zsh > _claude-container
            installShellCompletion claude-container.{bash,fish} _claude-container

            # Copy docker context files
            mkdir -p $out/share/claude-container
            cp -r ${./docker}/* $out/share/claude-container/

            # Wrap binary to ensure runtime deps on PATH
            wrapProgram $out/bin/claude-container \
              --prefix PATH : ${
                pkgs.lib.makeBinPath (
                  with pkgs;
                  [
                    git
                    docker
                  ]
                )
              } \
              --set CLAUDE_CONTAINER_DOCKER_CONTEXT "$out/share/claude-container"
          '';

          meta = with pkgs.lib; {
            description = "Run multiple Claude Code instances in isolated containers";
            homepage = "https://github.com/joegoldin/claude-container";
            license = licenses.mit;
            mainProgram = "claude-container";
          };
        };

        checks.default = pkgs.buildGoModule {
          pname = "claude-container-tests";
          version = "0.1.0";
          src = ./.;
          inherit vendorHash;
          nativeBuildInputs = [ pkgs.git ];
          doCheck = true;
          preCheck = ''
            export HOME=/tmp/claude-container-test-home
            mkdir -p $HOME
            git config --global user.email "test@test.com"
            git config --global user.name "Test"
            git config --global init.defaultBranch main
          '';
          installPhase = ''
            touch $out
          '';
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            git
            docker
          ];
        };
      }
    )
    // {
      overlays.default = final: prev: {
        claude-container = self.packages.${prev.system}.claude-container;
      };
    };
}
