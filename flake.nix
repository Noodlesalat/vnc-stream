{
  description = "VNC to M-JPEG Streamer HTTP Server";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "vnc-stream";
          version = "0.1.0";
          src = ./.;

          # vendorHash can be null since we resolve vnc2video locally via go.mod replace.
          vendorHash = null;

          subPackages = [ "." ];

          meta = with pkgs.lib; {
            description = "VNC screen MJPEG streamer";
            license = licenses.mit;
            platforms = platforms.linux ++ platforms.darwin;
          };
        };

        defaultPackage = self.packages.${system}.default;

        apps.default = flake-utils.lib.mkApp {
          drv = self.packages.${system}.default;
        };
      }
    ) // {
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.vnc-stream;
        in
        {
          options.services.vnc-stream = {
            enable = lib.mkEnableOption "VNC to M-JPEG Streamer service";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              description = "The vnc-stream package to run.";
            };

            configFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              description = "Path to JSON configuration file (recommended for secrets).";
            };

            host = lib.mkOption {
              type = lib.types.str;
              default = "localhost";
              description = "VNC server hostname/IP.";
            };

            port = lib.mkOption {
              type = lib.types.port;
              default = 5900;
              description = "VNC server port.";
            };

            password = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "VNC password. Note: use configFile if you want to avoid storing passwords in the world-readable Nix store.";
            };

            listen = lib.mkOption {
              type = lib.types.str;
              default = ":8080";
              description = "HTTP address and port to listen on.";
            };

            fps = lib.mkOption {
              type = lib.types.int;
              default = 10;
              description = "Target frame rate (Frames Per Second).";
            };

            quality = lib.mkOption {
              type = lib.types.int;
              default = 80;
              description = "JPEG compression quality (1-100).";
            };
          };

          config = lib.mkIf cfg.enable {
            systemd.services.vnc-stream = {
              description = "VNC to M-JPEG Streamer Service";
              after = [ "network.target" ];
              wantedBy = [ "multi-user.target" ];

              serviceConfig = {
                ExecStart =
                  let
                    args = lib.escapeShellArgs (
                      (lib.optionals (cfg.configFile != null) [ "-config" cfg.configFile ]) ++
                      [
                        "-host" cfg.host
                        "-port" (toString cfg.port)
                        "-listen" cfg.listen
                        "-fps" (toString cfg.fps)
                        "-quality" (toString cfg.quality)
                      ] ++ (lib.optionals (cfg.password != null) [ "-password" cfg.password ])
                    );
                  in
                  "${cfg.package}/bin/vnc-stream ${args}";
                Restart = "always";
                RestartSec = "5s";
                DynamicUser = true;
                PrivateTmp = true;
                ProtectSystem = "full";
              };
            };
          };
        };
    };
}
