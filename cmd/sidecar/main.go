package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	relaybridge "github.com/sTenshi058/AJB_MP_SIDECAR_PROXY"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/config"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/discovery"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/eos"
)

const version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sidecar:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("a command is required")
	}

	switch args[0] {
	case "version", "--version", "-v":
		_, err := fmt.Fprintf(stdout, "sidecar %s\n", version)
		return err
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "host":
		return runSessionCommand("host", args[1:], stdout, stderr)
	case "join":
		return runSessionCommand("join", args[1:], stdout, stderr)
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("config requires init or validate")
	}
	switch args[0] {
	case "init":
		flags := flag.NewFlagSet("config init", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("path", "", "config file path")
		force := flags.Bool("force", false, "replace an existing config file")
		if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("config init does not accept positional arguments")
		}
		resolvedPath, err := configPath(*path)
		if err != nil {
			return err
		}
		if err := config.Init(resolvedPath, *force); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "Created config scaffold: %s\nSet EOS IDs in that file and %s in your environment.\n", resolvedPath, config.ClientSecretEnv)
		return err

	case "validate":
		flags := flag.NewFlagSet("config validate", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("path", "", "config file path")
		if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("config validate does not accept positional arguments")
		}
		resolvedPath, err := configPath(*path)
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolvedPath)
		if err != nil {
			return err
		}
		if err := cfg.Validate(os.Getenv(config.ClientSecretEnv)); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "EOS config is complete: %s\n", resolvedPath)
		return err
	default:
		return fmt.Errorf("unknown config command %q (expected init or validate)", args[0])
	}
}

func runSessionCommand(command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "config file path")
	gameAddr := "127.0.0.1:1170"
	listenAddr := "127.0.0.1:1170"
	if command == "host" {
		flags.StringVar(&gameAddr, "game-addr", gameAddr, "local game UDP address")
	} else {
		flags.StringVar(&listenAddr, "listen-addr", listenAddr, "loopback UDP address for the joining game")
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if command == "host" {
		resolvedGameAddr, err := net.ResolveUDPAddr("udp", gameAddr)
		if err != nil {
			return fmt.Errorf("invalid --game-addr: %w", err)
		}
		if resolvedGameAddr.IP == nil || !resolvedGameAddr.IP.IsLoopback() {
			return errors.New("--game-addr must use a loopback address")
		}
	} else {
		resolvedListenAddr, err := net.ResolveUDPAddr("udp", listenAddr)
		if err != nil {
			return fmt.Errorf("invalid --listen-addr: %w", err)
		}
		if resolvedListenAddr.IP == nil || !resolvedListenAddr.IP.IsLoopback() {
			return errors.New("--listen-addr must use a loopback address")
		}
	}

	lobbyID := ""
	switch command {
	case "host":
		if flags.NArg() != 0 {
			return errors.New("host does not accept positional arguments")
		}
	case "join":
		if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
			return errors.New("join requires one EOS lobby ID")
		}
		lobbyID = flags.Arg(0)
	default:
		return fmt.Errorf("unsupported session command %q", command)
	}

	resolvedPath, err := configPath(*path)
	if err != nil {
		return err
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(os.Getenv(config.ClientSecretEnv)); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runEOSCommand(ctx, command, lobbyID, gameAddr, listenAddr, cfg, stdout)
}

func runEOSCommand(ctx context.Context, command, lobbyID, gameAddr, listenAddr string, cfg config.Config, stdout io.Writer) error {
	cacheDirectory, err := eosCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDirectory, 0700); err != nil {
		return fmt.Errorf("create EOS cache directory %q: %w", cacheDirectory, err)
	}
	client, err := eos.New(cfg, os.Getenv(config.ClientSecretEnv), cacheDirectory)
	if err != nil {
		return fmt.Errorf("initialize EOS: %w", err)
	}
	defer client.Close()

	transport, err := relaybridge.NewFragmentingTransport(client)
	if err != nil {
		return err
	}

	switch command {
	case "host":
		createdLobbyID, err := client.CreateLobby()
		if err != nil {
			return fmt.Errorf("create EOS lobby: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "Lobby ID: %s\n", createdLobbyID); err != nil {
			return fmt.Errorf("print lobby ID: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "Keep the host game listening on %s and this sidecar running while players are connected.\n", gameAddr); err != nil {
			return fmt.Errorf("print host instructions: %w", err)
		}
		bridge, err := relaybridge.NewHostBridge(gameAddr, transport)
		if err != nil {
			return err
		}
		return runWithDiscoveryMonitor(ctx, stdout, func(runCtx context.Context) error {
			if err := bridge.Run(runCtx); err != nil {
				return fmt.Errorf("run host UDP bridge: %w", err)
			}
			return nil
		})

	case "join":
		hostPeerID, err := client.JoinLobby(lobbyID)
		if err != nil {
			return fmt.Errorf("join EOS lobby: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "Joined EOS lobby %s. Local UDP bridge listening on %s.\nIn the game's console, connect with: open %s\n", lobbyID, listenAddr, listenAddr); err != nil {
			return fmt.Errorf("print join status: %w", err)
		}
		bridge, err := relaybridge.NewClientBridge(listenAddr, hostPeerID, transport)
		if err != nil {
			return err
		}
		defer bridge.Close()
		return runWithDiscoveryMonitor(ctx, stdout, func(runCtx context.Context) error {
			if err := bridge.Run(runCtx); err != nil {
				return fmt.Errorf("run client UDP bridge: %w", err)
			}
			return nil
		})
	default:
		return fmt.Errorf("unsupported session command %q", command)
	}
}

func runWithDiscoveryMonitor(ctx context.Context, output io.Writer, runBridge func(context.Context) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	monitor, err := discovery.StartUDPMonitor(runCtx, "0.0.0.0:14001", output)
	if err != nil {
		return fmt.Errorf("start UDP/14001 discovery monitor: %w", err)
	}
	defer monitor.Close()

	bridgeResult := make(chan error, 1)
	go func() {
		bridgeResult <- runBridge(runCtx)
	}()

	select {
	case err := <-bridgeResult:
		cancel()
		if err != nil {
			return err
		}
		return nil
	case err := <-monitor.Done():
		cancel()
		<-bridgeResult
		if err != nil {
			return fmt.Errorf("UDP/14001 discovery monitor stopped: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("UDP/14001 discovery monitor stopped unexpectedly")
	case <-ctx.Done():
		cancel()
		<-bridgeResult
		return nil
	}
}

func eosCachePath() (string, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(configDirectory, "ajb-sidecar", "eos-cache"), nil
}

func configPath(path string) (string, error) {
	if path != "" {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve config path: %w", err)
		}
		return absPath, nil
	}
	return config.DefaultPath()
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `AJB multiplayer sidecar

Usage:
  sidecar version
  sidecar config init [--path FILE] [--force]
  sidecar config validate [--path FILE]
  sidecar host [--config FILE] [--game-addr 127.0.0.1:1170]
  sidecar join [--config FILE] [--listen-addr 127.0.0.1:1170] LOBBY_ID`)
}
