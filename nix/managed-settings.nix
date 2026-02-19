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

  sandbox = {
    enabled = true;
    autoAllowBashIfSandboxed = true;
    enableWeakerNestedSandbox = true;
    allowUnsandboxedCommands = false;
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
    ];
  };
  permissions.deny = [
    "Read(/etc/shadow)"
    "Read(/etc/passwd)"
    "Read(~/.ssh/**)"
    "Read(~/.aws/**)"
    "Read(~/.gnupg/**)"
  ];
}
