{
  description = "Run multiple Claude Code instances in isolated containers";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    llm-agents = {
      url = "github:numtide/llm-agents.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      llm-agents,
    }:
    {
      lib.mkClaudeContainer =
        {
          pkgs,
          claude-code,
          settings ? { },
          managedSettings ? import ./nix/managed-settings.nix,
          extraPackages ? [ ],
          extraAllowCommands ? [ ],
        }:
        let
          system = pkgs.stdenv.hostPlatform.system;

          image = pkgs.callPackage ./nix/image.nix {
            inherit
              claude-code
              settings
              managedSettings
              extraPackages
              ;
          };

          proxyImage = pkgs.callPackage ./nix/proxy-image.nix { };

          # Auto-derive command names from extraPackages so they get allowed
          # in default/med security profiles. Uses meta.mainProgram when
          # available, falling back to pname or the name portion of the
          # derivation name.
          derivedCommands = map (
            pkg:
            let
              name = pkg.meta.mainProgram or pkg.pname or (builtins.parseDrvName pkg.name).name;
            in
            "${name} *"
          ) extraPackages;

          allExtraAllowCommands = derivedCommands ++ extraAllowCommands;

          cli = pkgs.symlinkJoin {
            name = "claude-container";
            paths = [ self.packages.${system}.claude-container-unwrapped ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            postBuild = ''
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
                --set CLAUDE_CONTAINER_IMAGE_TARBALL "${image}" \
                --set CLAUDE_CONTAINER_IMAGE_TAG "claude-code:latest" \
                --set CLAUDE_PROXY_IMAGE_TARBALL "${proxyImage}" \
                --set CLAUDE_PROXY_IMAGE_TAG "claude-proxy:latest" \
                --set CLAUDE_CONTAINER_EXTRA_ALLOW_COMMANDS '${builtins.toJSON allExtraAllowCommands}'

              # Create yacc alias pointing to wrapped binary
              ln -sf $out/bin/claude-container $out/bin/yacc
            '';
          };
        in
        { inherit image proxyImage cli; };
    }
    // flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ llm-agents.overlays.default ];
        };
        vendorHash = "sha256-usdgGSIdHT9L0ZwWVxN3Og3e14kTRskJTNGRQdMmwNY=";

        defaultContainer = self.lib.mkClaudeContainer {
          inherit pkgs;
          claude-code = pkgs.llm-agents.claude-code;
        };
      in
      {
        packages.default = defaultContainer.cli;
        packages.claude-container = defaultContainer.cli;
        packages.claude-container-image = defaultContainer.image;
        packages.claude-proxy-image = defaultContainer.proxyImage;

        packages.claude-container-unwrapped = pkgs.buildGoModule {
          pname = "claude-container";
          version = "0.1.0";
          src = ./.;
          inherit vendorHash;
          doCheck = false;

          nativeBuildInputs = with pkgs; [ installShellFiles ];

          postInstall = ''
            # Generate shell completions
            $out/bin/claude-container completion bash > claude-container.bash
            $out/bin/claude-container completion fish > claude-container.fish
            $out/bin/claude-container completion zsh > _claude-container
            installShellCompletion claude-container.{bash,fish} _claude-container
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
