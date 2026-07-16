{
  description = "Machtiani mct-agent";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);
    in {
      packages = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
          lib = pkgs.lib;
          revision = self.rev or "unknown";
          shortRevision = if revision == "unknown" then revision else builtins.substring 0 12 revision;
          source = lib.cleanSourceWith {
            src = ./.;
            filter = path: type:
              let rel = lib.removePrefix (toString ./. + "/") (toString path);
              in !(lib.hasPrefix "third_party/skyvern" rel)
                && !(lib.hasPrefix ".git" rel)
                && !(lib.hasPrefix ".gocache" rel)
                && rel != "result";
          };
          mct-agent = pkgs.buildGoModule {
            pname = "mct-agent";
            version = "0.1.0-${shortRevision}";
            src = source + "/agent";
            subPackages = [ "cmd/mct-agent" ];
            vendorHash = "sha256-yLAYUCr/2YU45qTd74K+C11dLBcxsbouNn+PWYJYuFw=";
            env.CGO_ENABLED = 0;
            ldflags = [
              "-s"
              "-w"
              "-X main.Version=dev-${shortRevision}"
              "-X main.Commit=${revision}"
              "-X main.BuiltAt=${self.lastModifiedDate or "unknown"}"
              "-X main.Dirty=clean"
            ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            nativeCheckInputs = [ pkgs.gitMinimal ];
            postInstall = ''
              wrapProgram $out/bin/mct-agent \
                --prefix PATH : ${lib.makeBinPath [
                  pkgs.gitMinimal
                  pkgs.ripgrep
                  pkgs.bashNonInteractive
                  pkgs.coreutils
                  pkgs.gnused
                ]}
            '';
          };
          install = pkgs.writeShellApplication {
            name = "mct-agent-install";
            runtimeInputs = [ pkgs.gitMinimal ];
            text = ''
              exec ${mct-agent}/bin/mct-agent install --source "$PWD" "$@"
            '';
          };
        in {
          inherit mct-agent install;
          default = mct-agent;
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.mct-agent}/bin/mct-agent";
        };
        mct-agent = self.apps.${system}.default;
        install = {
          type = "app";
          program = "${self.packages.${system}.install}/bin/mct-agent-install";
        };
      });

      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
          lib = pkgs.lib;
          portableGo = pkgs.go.overrideAttrs (old: {
            patches = builtins.filter (patch:
              let name = builtins.baseNameOf (toString patch);
              in !(lib.hasInfix "iana-etc-" name)
                && !(lib.hasInfix "mailcap-" name)
                && !(lib.hasInfix "tzdata-" name)
            ) old.patches;
          });
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.gitMinimal pkgs.ripgrep pkgs.bash pkgs.coreutils pkgs.gnused ];
            shellHook = ''export PATH=${lib.makeBinPath [ pkgs.go pkgs.gitMinimal pkgs.ripgrep pkgs.bash pkgs.coreutils pkgs.gnused ]}:$PATH'';
          };
          smoke = pkgs.mkShell {
            packages = [
              pkgs.gitMinimal
              pkgs.jq
              pkgs.bash
              pkgs.coreutils
              pkgs.gnused
              pkgs.curl
              pkgs.expect
              pkgs.python3
              pkgs.go
            ];
            shellHook = ''export PATH=${lib.makeBinPath [ pkgs.go pkgs.gitMinimal pkgs.jq pkgs.bash pkgs.coreutils pkgs.gnused pkgs.curl pkgs.expect pkgs.python3 ]}:$PATH'';
          };
          bench = pkgs.mkShell {
            packages = [
              portableGo
              pkgs.gitMinimal
              pkgs.bash
              pkgs.coreutils
              pkgs.file
            ];
            shellHook = ''export PATH=${lib.makeBinPath [ portableGo pkgs.gitMinimal pkgs.bash pkgs.coreutils pkgs.file ]}:$PATH'';
          };
        });

      checks = forAllSystems (system: {
        inherit (self.packages.${system}) mct-agent;
      });
    };
}
