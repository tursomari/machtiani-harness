{
  description = "Machtiani agent";

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
          patchedGo = pkgs.go.overrideAttrs (_: {
            version = "1.26.5";
            src = pkgs.fetchurl {
              url = "https://go.dev/dl/go1.26.5.src.tar.gz";
              hash = "sha256-SVvkvIcXasVnOS5bQRar2YRm0z17SdQedkzMaXay3EI=";
            };
          });
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
          machtiani = (pkgs.buildGoModule.override { go = patchedGo; }) {
            pname = "machtiani";
            version = "0.1.0-${shortRevision}";
            src = source + "/agent";
            subPackages = [ "cmd/machtiani" ];
            vendorHash = "sha256-BZL0+ldXx7WqMrcLGsxkX1KZ+9GO9AuVukmIJWiY7Zw=";
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
              wrapProgram $out/bin/machtiani \
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
            name = "machtiani-install";
            runtimeInputs = [ pkgs.gitMinimal ];
            text = ''
              exec ${machtiani}/bin/machtiani install --source "$PWD" "$@"
            '';
          };
        in {
          inherit machtiani install;
          default = machtiani;
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.machtiani}/bin/machtiani";
        };
        machtiani = self.apps.${system}.default;
        install = {
          type = "app";
          program = "${self.packages.${system}.install}/bin/machtiani-install";
        };
      });

      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
          lib = pkgs.lib;
          patchedGo = pkgs.go.overrideAttrs (_: {
            version = "1.26.5";
            src = pkgs.fetchurl {
              url = "https://go.dev/dl/go1.26.5.src.tar.gz";
              hash = "sha256-SVvkvIcXasVnOS5bQRar2YRm0z17SdQedkzMaXay3EI=";
            };
          });
          portableGo = patchedGo.overrideAttrs (old: {
            patches = builtins.filter (patch:
              let name = builtins.baseNameOf (toString patch);
              in !(lib.hasInfix "iana-etc-" name)
                && !(lib.hasInfix "mailcap-" name)
                && !(lib.hasInfix "tzdata-" name)
            ) old.patches;
          });
        in {
          default = pkgs.mkShell {
            packages = [ patchedGo pkgs.gitMinimal pkgs.ripgrep pkgs.bash pkgs.coreutils pkgs.gnused ];
            shellHook = ''export PATH=${lib.makeBinPath [ patchedGo pkgs.gitMinimal pkgs.ripgrep pkgs.bash pkgs.coreutils pkgs.gnused ]}:$PATH'';
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
              patchedGo
            ];
            shellHook = ''export PATH=${lib.makeBinPath [ patchedGo pkgs.gitMinimal pkgs.jq pkgs.bash pkgs.coreutils pkgs.gnused pkgs.curl pkgs.expect pkgs.python3 ]}:$PATH'';
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
        inherit (self.packages.${system}) machtiani;
      });
    };
}
