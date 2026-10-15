package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	relaybridge "github.com/sTenshi058/AJB_MP_SIDECAR_PROXY"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/config"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/discovery"
	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/eos"
)

const version = "0.1.0-dev"

var bundleKeyHex string

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
	case "package":
		return runPackage(args[1:], stdout, stderr)
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
	case "bundle":
		flags := flag.NewFlagSet("config bundle", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("config", "", "config file path containing EOS IDs")
		output := flags.String("out", "", "output config path for the distributable bundle")
		if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("config bundle does not accept positional arguments")
		}
		if *output == "" {
			return errors.New("config bundle requires --out")
		}
		resolvedPath, err := configPath(*path)
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolvedPath)
		if err != nil {
			return err
		}
		secret := os.Getenv(config.ClientSecretEnv)
		if strings.TrimSpace(secret) == "" {
			secret, err = cfg.ResolveClientSecret("", bundleKeyHex)
			if err != nil {
				return err
			}
		}
		if err := cfg.Validate(secret); err != nil {
			return err
		}
		cfg.ClientSecret = ""
		cfg.EncryptedClientSecret, err = config.EncryptClientSecret(secret, bundleKeyHex)
		if err != nil {
			return err
		}
		if err := writeBundleConfig(*output, cfg); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "Created encrypted EOS config: %s\nThe sidecar build key is embedded in the executable; encryption only obscures the secret from casual inspection.\n", *output)
		return err

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
		_, err = fmt.Fprintf(stdout, "Created config scaffold: %s\nSet EOS IDs in that file; provide the client secret through %s or a local client_secret field.\n", resolvedPath, config.ClientSecretEnv)
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
		secret, err := cfg.ResolveClientSecret(os.Getenv(config.ClientSecretEnv), bundleKeyHex)
		if err != nil {
			return err
		}
		if err := cfg.Validate(secret); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "EOS config is complete: %s\n", resolvedPath)
		return err
	default:
		return fmt.Errorf("unknown config command %q (expected init or validate)", args[0])
	}
}

func runPackage(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("package", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputDir := flags.String("out", "", "output directory for the distributable bundle (defaults beside the executable)")
	configPathFlag := flags.String("config", "", "config file path containing EOS IDs")
	archivePath := flags.String("archive", "", "optional ZIP archive output path")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("package does not accept positional arguments")
	}

	secret := os.Getenv(config.ClientSecretEnv)
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("%s must be set in the environment to create a distribution bundle", config.ClientSecretEnv)
	}
	if bundleKeyHex == "" {
		return errors.New("this executable has no config encryption key; rebuild with scripts/build.sh before packaging")
	}
	resolvedConfigPath, err := configPath(*configPathFlag)
	if err != nil {
		return err
	}
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(secret); err != nil {
		return err
	}
	cfg.ClientSecret = ""
	cfg.EncryptedClientSecret, err = config.EncryptClientSecret(secret, bundleKeyHex)
	if err != nil {
		return err
	}
	resolvedOutputDir := *outputDir
	if resolvedOutputDir == "" {
		executablePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve sidecar executable path: %w", err)
		}
		resolvedOutputDir = filepath.Join(filepath.Dir(executablePath), "package")
	}
	binaryName, libraryName, err := distributionNames(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if _, err := os.Stat(resolvedOutputDir); err == nil {
		return fmt.Errorf("output directory %q already exists; choose a new path or remove that specific directory first", resolvedOutputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check output directory %q: %w", resolvedOutputDir, err)
	}

	if err := os.MkdirAll(resolvedOutputDir, 0700); err != nil {
		return fmt.Errorf("create package directory %q: %w", resolvedOutputDir, err)
	}
	if err := copyDistributionFiles(resolvedOutputDir, binaryName, libraryName); err != nil {
		return err
	}
	if err := writeBundleConfig(filepath.Join(resolvedOutputDir, "config.json"), cfg); err != nil {
		return err
	}
	if *archivePath != "" {
		if err := writeBundleArchive(resolvedOutputDir, *archivePath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Created %s bundle in %s\nShare the entire folder; config.json contains an encrypted client secret.\n", binaryName, resolvedOutputDir); err != nil {
		return err
	}
	if *archivePath != "" {
		if _, err := fmt.Fprintf(stdout, "Created release archive: %s\n", *archivePath); err != nil {
			return err
		}
	}
	return nil
}

func writeBundleArchive(bundleDir, archivePath string) error {
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create release archive %q: %w", archivePath, err)
	}
	writer := zip.NewWriter(archive)
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		_ = writer.Close()
		_ = archive.Close()
		_ = os.Remove(archivePath)
		return fmt.Errorf("read bundle directory %q: %w", bundleDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("unexpected directory in bundle: %q", entry.Name())
		}
		path := filepath.Join(bundleDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("inspect bundle file %q: %w", path, err)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("create archive entry for %q: %w", path, err)
		}
		header.Name = entry.Name()
		header.Method = zip.Deflate
		output, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("write archive entry for %q: %w", path, err)
		}
		input, err := os.Open(path)
		if err != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("open bundle file %q: %w", path, err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if copyErr != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("archive bundle file %q: %w", path, copyErr)
		}
		if closeErr != nil {
			_ = writer.Close()
			_ = archive.Close()
			_ = os.Remove(archivePath)
			return fmt.Errorf("close bundle file %q: %w", path, closeErr)
		}
	}
	if err := writer.Close(); err != nil {
		_ = archive.Close()
		_ = os.Remove(archivePath)
		return fmt.Errorf("finalize release archive %q: %w", archivePath, err)
	}
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("close release archive %q: %w", archivePath, err)
	}
	return nil
}

func writeBundleConfig(path string, cfg config.Config) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create bundle config %q: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		_ = file.Close()
		return fmt.Errorf("write bundle config %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync bundle config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close bundle config %q: %w", path, err)
	}
	return nil
}

func distributionNames(goos, goarch string) (string, string, error) {
	switch goos {
	case "windows":
		return "sidecar.exe", "EOSSDK-Win64-Shipping.dll", nil
	case "linux":
		switch goarch {
		case "amd64":
			return "sidecar", "libEOSSDK-Linux-Shipping.so", nil
		case "arm64":
			return "sidecar", "libEOSSDK-LinuxArm64-Shipping.so", nil
		default:
			return "", "", fmt.Errorf("distribution packaging is not configured for Linux/%s", goarch)
		}
	case "darwin":
		return "sidecar", "libEOSSDK-Mac-Shipping.dylib", nil
	default:
		return "", "", fmt.Errorf("distribution packaging is not configured for %s", goos)
	}
}

func copyDistributionFiles(outputDir, binaryName, libraryName string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve sidecar executable path: %w", err)
	}
	for _, name := range []string{binaryName, libraryName} {
		sourcePath := filepath.Join(filepath.Dir(executablePath), name)
		source, err := os.Open(sourcePath)
		if err != nil {
			return fmt.Errorf("open distribution dependency %q: %w", sourcePath, err)
		}
		destinationPath := filepath.Join(outputDir, name)
		destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("create distribution file %q: %w", destinationPath, err)
		}
		_, copyErr := io.Copy(destination, source)
		sourceCloseErr := source.Close()
		destinationCloseErr := destination.Close()
		if copyErr != nil {
			return fmt.Errorf("copy distribution dependency %q: %w", name, copyErr)
		}
		if sourceCloseErr != nil {
			return fmt.Errorf("close distribution dependency %q: %w", name, sourceCloseErr)
		}
		if destinationCloseErr != nil {
			return fmt.Errorf("close distribution file %q: %w", destinationPath, destinationCloseErr)
		}
	}
	if err := writeDistributionReadme(filepath.Join(outputDir, "README.txt")); err != nil {
		return fmt.Errorf("copy bundle instructions: %w", err)
	}
	return nil
}

func writeDistributionReadme(path string) error {
	const instructions = `AJB Multiplayer Sidecar

This folder contains the sidecar and EOS runtime required to use your host's EOS configuration.
Keep every file together. Start the AJB game before starting a host session.

Windows:
  Host: sidecar.exe host
  Join: sidecar.exe join LOBBY_ID
The package includes the EOS runtime DLL and statically links the MinGW runtime.

Linux:
  Host: ./sidecar host
  Join: ./sidecar join LOBBY_ID

The host should share the printed lobby ID with the joining player. Both players must keep the
sidecar running. In AJB, search for LAN games and select "AJB Sidecar Relay".

config.json contains an encrypted EOS client secret. The sidecar includes the key needed to
decrypt it, so a recipient can recover and reuse the secret. Do not publish this folder or share
it outside the intended test group. The client policy should have only the permissions needed
by this app.
`
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	if _, err := io.WriteString(file, instructions); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	return nil
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
		if resolvedGameAddr.Port < 1 {
			return errors.New("--game-addr must use a port between 1 and 65535")
		}
	} else {
		resolvedListenAddr, err := net.ResolveUDPAddr("udp", listenAddr)
		if err != nil {
			return fmt.Errorf("invalid --listen-addr: %w", err)
		}
		if resolvedListenAddr.IP == nil || !resolvedListenAddr.IP.IsLoopback() || resolvedListenAddr.IP.To4() == nil {
			return errors.New("--listen-addr must use an IPv4 loopback address")
		}
		if resolvedListenAddr.Port < 1 {
			return errors.New("--listen-addr must use a port between 1 and 65535")
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
	secret, err := cfg.ResolveClientSecret(os.Getenv(config.ClientSecretEnv), bundleKeyHex)
	if err != nil {
		return err
	}
	if err := cfg.Validate(secret); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runEOSCommand(ctx, command, lobbyID, gameAddr, listenAddr, cfg, secret, stdout)
}

func runEOSCommand(ctx context.Context, command, lobbyID, gameAddr, listenAddr string, cfg config.Config, clientSecret string, stdout io.Writer) error {
	cacheDirectory, err := eosCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDirectory, 0700); err != nil {
		return fmt.Errorf("create EOS cache directory %q: %w", cacheDirectory, err)
	}
	client, err := eos.New(cfg, clientSecret, cacheDirectory)
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
		advertisement, err := newDiscoveryAdvertisement(gameAddr)
		if err != nil {
			return err
		}
		return runWithDiscoveryResponder(ctx, stdout, advertisement, func(runCtx context.Context) error {
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
		if _, err := fmt.Fprintf(stdout, "Joined EOS lobby %s. Local UDP bridge listening on %s.\nThe sidecar will answer the game's LAN server search; direct-connect fallback: open %s\n", lobbyID, listenAddr, listenAddr); err != nil {
			return fmt.Errorf("print join status: %w", err)
		}
		bridge, err := relaybridge.NewClientBridge(listenAddr, hostPeerID, transport)
		if err != nil {
			return err
		}
		defer bridge.Close()
		advertisement, err := newDiscoveryAdvertisement(listenAddr)
		if err != nil {
			return err
		}
		return runWithDiscoveryResponder(ctx, stdout, advertisement, func(runCtx context.Context) error {
			if err := bridge.Run(runCtx); err != nil {
				return fmt.Errorf("run client UDP bridge: %w", err)
			}
			return nil
		})
	default:
		return fmt.Errorf("unsupported session command %q", command)
	}
}

func newDiscoveryAdvertisement(endpoint string) (discovery.Advertisement, error) {
	addr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return discovery.Advertisement{}, fmt.Errorf("resolve discovery endpoint %q: %w", endpoint, err)
	}
	loopbackAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: addr.Port}
	return discovery.NewAdvertisement("AJB Sidecar Relay", loopbackAddr)
}

func runWithDiscoveryResponder(ctx context.Context, output io.Writer, advertisement discovery.Advertisement, runBridge func(context.Context) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	responder, err := discovery.StartUDPResponder(runCtx, "0.0.0.0:14001", advertisement, output)
	if err != nil {
		return fmt.Errorf("start UDP/14001 discovery responder: %w", err)
	}
	defer responder.Close()

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
	case err := <-responder.Done():
		cancel()
		<-bridgeResult
		if err != nil {
			return fmt.Errorf("UDP/14001 discovery responder stopped: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("UDP/14001 discovery responder stopped unexpectedly")
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
  sidecar config bundle --config FILE --out FILE
  sidecar package [--config FILE] [--out DIR]
  sidecar host [--config FILE] [--game-addr 127.0.0.1:1170]
  sidecar join [--config FILE] [--listen-addr 127.0.0.1:1170] LOBBY_ID`)
}
