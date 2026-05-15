{ pkgs, ... }:

{
  languages.go.enable = true;

  packages = with pkgs; [
    git
    docker
  ];
}
