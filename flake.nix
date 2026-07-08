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

            instances = lib.mkOption {
              type = lib.types.attrsOf (lib.types.submodule {
                options = {
                  enable = lib.mkOption {
                    type = lib.types.bool;
                    default = true;
                    description = "Enable this vnc-stream instance.";
                  };

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
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = "VNC server hostname/IP.";
                  };

                  port = lib.mkOption {
                    type = lib.types.nullOr lib.types.port;
                    default = null;
                    description = "VNC server port.";
                  };

                  password = lib.mkOption {
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = "VNC password. Note: use configFile if you want to avoid storing passwords in the world-readable Nix store.";
                  };

                  listen = lib.mkOption {
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = "HTTP address and port to listen on.";
                  };

                  fps = lib.mkOption {
                    type = lib.types.nullOr lib.types.int;
                    default = null;
                    description = "Target frame rate (Frames Per Second).";
                  };

                  quality = lib.mkOption {
                    type = lib.types.nullOr lib.types.int;
                    default = null;
                    description = "JPEG compression quality (1-100).";
                  };

                  bpp = lib.mkOption {
                    type = lib.types.nullOr lib.types.int;
                    default = null;
                    description = "VNC connection color depth in bits per pixel (8, 16, or 32).";
                  };
                };
              });
              default = {};
              description = "VNC stream instances configuration.";
            };
          };

          config = lib.mkIf cfg.enable {
            users.groups.vnc-stream = {};
            users.users.vnc-stream = {
              description = "VNC to M-JPEG Streamer service user";
              isSystemUser = true;
              group = "vnc-stream";
            };

            systemd.services = lib.mapAttrs' (name: instanceCfg:
              lib.nameValuePair "vnc-stream-${name}" (lib.mkIf instanceCfg.enable {
                description = "VNC to M-JPEG Streamer Service - ${name}";
                after = [ "network.target" ];
                wantedBy = [ "multi-user.target" ];

                serviceConfig = {
                  ExecStart =
                    let
                      args = lib.escapeShellArgs (
                        (lib.optionals (instanceCfg.configFile != null) [ "-config" instanceCfg.configFile ]) ++
                        (lib.optionals (instanceCfg.host != null) [ "-host" instanceCfg.host ]) ++
                        (lib.optionals (instanceCfg.port != null) [ "-port" (toString instanceCfg.port) ]) ++
                        (lib.optionals (instanceCfg.listen != null) [ "-listen" instanceCfg.listen ]) ++
                        (lib.optionals (instanceCfg.fps != null) [ "-fps" (toString instanceCfg.fps) ]) ++
                        (lib.optionals (instanceCfg.quality != null) [ "-quality" (toString instanceCfg.quality) ]) ++
                        (lib.optionals (instanceCfg.bpp != null) [ "-bpp" (toString instanceCfg.bpp) ]) ++
                        (lib.optionals (instanceCfg.password != null) [ "-password" instanceCfg.password ])
                      );
                    in
                    "${instanceCfg.package}/bin/vnc-stream ${args}";
                  Restart = "always";
                  RestartSec = "5s";
                  User = "vnc-stream";
                  Group = "vnc-stream";
                  PrivateTmp = true;
                  ProtectSystem = "full";
                };
              })
            ) cfg.instances;
          };
        };
    };
}
