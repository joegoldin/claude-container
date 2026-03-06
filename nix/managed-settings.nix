# nix/managed-settings.nix
{
  env = {
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1";
    DISABLE_AUTOUPDATER = "1";
  };

  cleanupPeriodDays = 14;

  alwaysThinkingEnabled = true;
  showTurnDuration = true;
  spinnerTipsEnabled = false;

  # Instructions for Claude about the container environment
  apiInstructions = "This container uses Nix for package management. Install software with: nix profile install nixpkgs#<package> (e.g., nix profile install nixpkgs#rustc nixpkgs#cargo). Search with: nix search nixpkgs <query>. List installed: nix profile list. Remove: nix profile remove <index>. Do not use apt-get, yum, brew, or other package managers.";

  sandbox = {
    enabled = true;
    autoAllowBashIfSandboxed = true;
    enableWeakerNestedSandbox = true;
    allowUnsandboxedCommands = true;
    excludedCommands = [ "git" ];
    network.allowedDomains = [
      "api.anthropic.com"
      "statsig.anthropic.com"
      "sentry.io"
      "github.com"
      "*.github.com"
      "*.npmjs.org"
      "registry.npmjs.org"
      "registry.yarnpkg.com"
      "pypi.org"
      "*.pypi.org"
      "files.pythonhosted.org"
      "cache.nixos.org"
      "*.cache.nixos.org"
      "channels.nixos.org"
    ];
  };
  permissions.allow = [
    "Bash(nix profile install *)"
    "Bash(nix profile remove *)"
    "Bash(nix profile list *)"
    "Bash(nix search *)"
  ];
  permissions.deny = [
    "Read(/etc/shadow)"
    "Read(/etc/passwd)"
    "Read(~/.ssh/**)"
    "Read(~/.aws/**)"
    "Read(~/.gnupg/**)"
  ];
}
