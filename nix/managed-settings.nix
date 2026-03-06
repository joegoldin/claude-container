# nix/managed-settings.nix
# Baked-in fallback — overridden by Go-generated managed-settings.json at runtime.
{
  env = {
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1";
    DISABLE_AUTOUPDATER = "1";
  };

  cleanupPeriodDays = 14;

  alwaysThinkingEnabled = true;
  showTurnDuration = true;
  spinnerTipsEnabled = false;

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

  defaultMode = "dontAsk";

  permissions.allow = [
    "Bash"
    "Read"
    "Edit"
    "Write"
    "WebFetch"
    "Grep"
    "Glob"
    "LS"
    "MultiEdit"
    "NotebookRead"
    "NotebookEdit"
    "TodoRead"
    "TodoWrite"
    "WebSearch"
  ];
  permissions.deny = [
    "Read(/etc/shadow)"
    "Read(/etc/passwd)"
    "Read(~/.ssh/**)"
    "Read(~/.aws/**)"
    "Read(~/.gnupg/**)"
  ];
}
