//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultConfig = ""
	evKey         = 0x01
	evAbs         = 0x03
)

var version = "0.1.0"

const usage = `chordio - Linux gamepad chord launcher

Usage:
  chordio [--config PATH] [--debug] [--verbose]
  chordio --list-devices [--list-all-devices]
  chordio test-input [--config PATH] [--device PATH ...] [--all-devices] [--verbose]
  chordio update [--force]
  chordio uninstall
  chordio version

Commands:
  test-input       Print gamepad input events and their evdev codes.
  update           Download and install the latest chordio release.
  uninstall        Interactively remove chordio components.
  version          Print version.

Config is searched in this order when --config is omitted:
  $CHORDIO_CONFIG, /etc/chordio/config.json, ~/.config/chordio/config.json
`

type Config struct {
	ScanIntervalSec int      `json:"scan_interval_sec,omitempty"`
	Devices         []string `json:"devices,omitempty"`
	AllowAllDevices bool     `json:"allow_all_devices,omitempty"`
	Chords          []Chord  `json:"chords"`

	path string
}

type Chord struct {
	Name       string   `json:"name"`
	Chord      []string `json:"chord"`
	Inputs     []string `json:"inputs,omitempty"`
	Enabled    *bool    `json:"enabled,omitempty"`
	CooldownMS int      `json:"cooldown_ms,omitempty"`
	Action     Action   `json:"action"`
}

type Action struct {
	Type    string   `json:"type"`
	Command []string `json:"command,omitempty"`
	Shell   string   `json:"shell,omitempty"`
	Path    string   `json:"path,omitempty"`
	Args    []string `json:"args,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Op      string   `json:"op,omitempty"`
	User    bool     `json:"user,omitempty"`
}

type chordRuntime struct {
	chord    Chord
	codes    []uint16
	held     bool
	lastFire time.Time
}

type inputEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

type stringListFlag []string

type watchedDevice struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type inputDeviceInfo struct {
	EventPath   string
	StablePaths []string
	Name        string
	Phys        string
	Uniq        string
	Bus         string
	Vendor      string
	Product     string
	Version     string
	Handlers    []string
	Readable    bool
	ReadError   string
}

var keyCodes = map[string]uint16{
	"BTN_A":      0x130,
	"BTN_B":      0x131,
	"BTN_C":      0x132,
	"BTN_X":      0x133,
	"BTN_Y":      0x134,
	"BTN_Z":      0x135,
	"BTN_SOUTH":  0x130,
	"BTN_EAST":   0x131,
	"BTN_NORTH":  0x133,
	"BTN_WEST":   0x134,
	"BTN_TL":     0x136,
	"BTN_TR":     0x137,
	"BTN_TL2":    0x138,
	"BTN_TR2":    0x139,
	"BTN_SELECT": 0x13a,
	"BTN_START":  0x13b,
	"BTN_MODE":   0x13c,
	"BTN_THUMBL": 0x13d,
	"BTN_THUMBR": 0x13e,
}

var codeNames = reverseKeyCodes()

var eventTypeNames = map[uint16]string{
	evKey: "EV_KEY",
	evAbs: "EV_ABS",
}

var absCodeNames = map[uint16]string{
	0x00: "ABS_X",
	0x01: "ABS_Y",
	0x02: "ABS_Z",
	0x03: "ABS_RX",
	0x04: "ABS_RY",
	0x05: "ABS_RZ",
	0x06: "ABS_THROTTLE",
	0x07: "ABS_RUDDER",
	0x08: "ABS_WHEEL",
	0x09: "ABS_GAS",
	0x0a: "ABS_BRAKE",
	0x10: "ABS_HAT0X",
	0x11: "ABS_HAT0Y",
	0x12: "ABS_HAT1X",
	0x13: "ABS_HAT1Y",
	0x14: "ABS_HAT2X",
	0x15: "ABS_HAT2Y",
	0x16: "ABS_HAT3X",
	0x17: "ABS_HAT3Y",
}

func main() {
	log.SetFlags(log.LstdFlags)

	if len(os.Args) > 1 && (os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Print(usage)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("chordio", version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "update" {
		force := false
		upFs := flag.NewFlagSet("update", flag.ExitOnError)
		upFs.BoolVar(&force, "force", false, "reinstall even if already on the latest version")
		if err := upFs.Parse(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		if err := cmdUpdate(force); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "uninstall" {
		if err := cmdUninstall(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "test-input" {
		if err := runTestInputCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	configFlag := flag.String("config", defaultConfig, "path to config file")
	listDevices := flag.Bool("list-devices", false, "list likely gamepad input devices and stable paths")
	listAllDevices := flag.Bool("list-all-devices", false, "include non-gamepad input devices in --list-devices output")
	testInput := flag.Bool("test-input", false, "print gamepad input events and their evdev codes")
	versionFlag := flag.Bool("version", false, "print version")
	debug := flag.Bool("debug", false, "print key events and do not run actions")
	verbose := flag.Bool("verbose", false, "log skipped devices and read errors")
	flag.Parse()

	if *versionFlag {
		fmt.Println("chordio", version)
		return
	}

	if *listDevices {
		if err := printDevices(*listAllDevices); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *testInput {
		if err := runTestInput(*configFlag, nil, false, *verbose); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := LoadConfig(*configFlag)
	if err != nil {
		log.Fatal(err)
	}
	runtimes, err := compileChords(cfg.Chords)
	if err != nil {
		log.Fatal(err)
	}
	if len(runtimes) == 0 {
		log.Fatal("no enabled chords configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.path != "" {
		log.Printf("Config: %s", cfg.path)
	}
	if *debug {
		log.Printf("Debug mode: printing key press/release events and not running actions")
	}
	for _, runtime := range runtimes {
		log.Printf("Chord %q: %s -> %s", runtime.chord.Name, formatChord(runtime.codes), runtime.chord.Action.describe())
	}
	if cfg.AllowAllDevices {
		log.Printf("Unsafe device mode enabled; scanning /dev/input/event*")
	} else if len(cfg.Devices) > 0 {
		log.Printf("Devices: %s", strings.Join(cfg.Devices, ", "))
	}

	interval := time.Duration(cfg.ScanIntervalOrDefault()) * time.Second
	if err := scanLoop(ctx, cfg.Devices, cfg.AllowAllDevices, runtimes, interval, *debug, *verbose); err != nil {
		log.Fatal(err)
	}
}

func logf(format string, args ...any) {
	log.Printf(format, args...)
}

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*f = append(*f, value)
	}
	return nil
}

func runTestInputCommand(args []string) error {
	fs := flag.NewFlagSet("test-input", flag.ExitOnError)
	configFlag := fs.String("config", defaultConfig, "path to config file")
	allDevices := fs.Bool("all-devices", false, "listen to every /dev/input/event* device")
	verbose := fs.Bool("verbose", false, "log skipped devices and read errors")
	var devices stringListFlag
	fs.Var(&devices, "device", "device path to listen to; may be repeated")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runTestInput(*configFlag, devices, *allDevices, *verbose)
}

func runTestInput(configPath string, devices []string, allDevices bool, verbose bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	configuredDevices := devices
	allowAllDevices := allDevices
	if len(configuredDevices) == 0 && configPath != "" {
		cfg, err := LoadConfig(configPath)
		if err != nil {
			return err
		}
		configuredDevices = cfg.Devices
		allowAllDevices = cfg.AllowAllDevices
	} else if len(configuredDevices) == 0 && configPath == "" {
		cfg, err := LoadConfig("")
		if err == nil {
			configuredDevices = cfg.Devices
			allowAllDevices = cfg.AllowAllDevices
			if cfg.path != "" {
				log.Printf("Config: %s", cfg.path)
			}
		}
	}

	if len(configuredDevices) == 0 && !allowAllDevices {
		devices, err := likelyGamepadPaths()
		if err != nil {
			return err
		}
		configuredDevices = devices
	}
	if len(configuredDevices) == 0 && !allowAllDevices {
		return errors.New("no likely gamepads found; run 'chordio --list-devices' or pass 'chordio test-input --device /dev/input/eventX'")
	}

	log.Printf("Input test mode: press controller buttons or move sticks/triggers; press Ctrl-C to stop")
	return testInputLoop(ctx, configuredDevices, allowAllDevices, verbose)
}

func likelyGamepadPaths() ([]string, error) {
	devices, err := discoverInputDevices()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, dev := range devices {
		if !dev.likelyGamepad() {
			continue
		}
		if len(dev.StablePaths) > 0 {
			paths = append(paths, dev.StablePaths[0])
		} else {
			paths = append(paths, dev.EventPath)
		}
	}
	return paths, nil
}

func (c *Config) ScanIntervalOrDefault() int {
	if c.ScanIntervalSec > 0 {
		return c.ScanIntervalSec
	}
	return 2
}

func configSearchPaths() []string {
	var paths []string
	if env := os.Getenv("CHORDIO_CONFIG"); env != "" {
		paths = append(paths, env)
	}
	paths = append(paths, "/etc/chordio/config.json")
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "chordio", "config.json"))
	}
	return paths
}

func LoadConfig(path string) (*Config, error) {
	if path == "" {
		for _, p := range configSearchPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		return nil, fmt.Errorf("no config file found; checked %s", strings.Join(configSearchPaths(), ", "))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	c.path = path
	return &c, nil
}

func printDevices(includeAll bool) error {
	devices, err := discoverInputDevices()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		fmt.Println("No /dev/input/event* devices found.")
		return nil
	}

	fmt.Println("Chordio input devices")
	fmt.Println()
	fmt.Println("Use a stable /dev/input/by-id or /dev/input/by-path entry in config when one is available:")
	fmt.Println()
	fmt.Println(`  "devices": ["/dev/input/by-id/...-event-joystick"]`)
	fmt.Println()

	shown := 0
	for _, dev := range devices {
		if !includeAll && !dev.likelyGamepad() {
			continue
		}
		shown++
		printDevice(dev)
	}

	if shown == 0 && !includeAll {
		fmt.Println("No likely gamepads found. Re-run with --list-devices --list-all-devices to inspect every input device.")
	}
	return nil
}

func printDevice(dev inputDeviceInfo) {
	status := "readable"
	if !dev.Readable {
		status = "not readable"
		if dev.ReadError != "" {
			status += ": " + dev.ReadError
		}
	}
	kind := "other"
	switch {
	case dev.looksKeyboard():
		kind = "keyboard-like, skip unless you know exactly why"
	case dev.likelyGamepad():
		kind = "likely gamepad"
	}

	fmt.Printf("- %s\n", dev.EventPath)
	if dev.Name != "" {
		fmt.Printf("  name: %s\n", dev.Name)
	}
	fmt.Printf("  kind: %s\n", kind)
	fmt.Printf("  access: %s\n", status)
	if ids := dev.idSummary(); ids != "" {
		fmt.Printf("  id: %s\n", ids)
	}
	if dev.Phys != "" {
		fmt.Printf("  phys: %s\n", dev.Phys)
	}
	if len(dev.Handlers) > 0 {
		fmt.Printf("  handlers: %s\n", strings.Join(dev.Handlers, " "))
	}
	if len(dev.StablePaths) > 0 {
		fmt.Println("  stable paths:")
		for _, path := range dev.StablePaths {
			fmt.Printf("    %s\n", path)
		}
	}
	fmt.Println()
}

func discoverInputDevices() ([]inputDeviceInfo, error) {
	stablePaths, err := inputStableSymlinks()
	if err != nil {
		return nil, err
	}

	devices, err := procInputDevices(stablePaths)
	if err == nil && len(devices) > 0 {
		return devices, nil
	}

	paths, globErr := filepath.Glob("/dev/input/event*")
	if globErr != nil {
		return nil, globErr
	}
	var fallback []inputDeviceInfo
	for _, path := range paths {
		fallback = append(fallback, inputDeviceInfo{
			EventPath:   path,
			StablePaths: stablePaths[cleanDevicePath(path)],
			Readable:    canReadDevice(path),
			ReadError:   readDeviceError(path),
		})
	}
	if err != nil && len(fallback) == 0 {
		return nil, err
	}
	return fallback, nil
}

func inputStableSymlinks() (map[string][]string, error) {
	out := make(map[string][]string)
	for _, pattern := range []string{"/dev/input/by-id/*", "/dev/input/by-path/*"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			out[cleanDevicePath(target)] = append(out[cleanDevicePath(target)], path)
		}
	}
	for target := range out {
		slices.Sort(out[target])
	}
	return out, nil
}

func procInputDevices(stablePaths map[string][]string) ([]inputDeviceInfo, error) {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err != nil {
		return nil, err
	}
	blocks := strings.Split(strings.TrimSpace(string(data)), "\n\n")
	var devices []inputDeviceInfo
	for _, block := range blocks {
		dev := parseProcInputBlock(block)
		if dev.EventPath == "" {
			continue
		}
		clean := cleanDevicePath(dev.EventPath)
		dev.StablePaths = stablePaths[clean]
		dev.Readable = canReadDevice(dev.EventPath)
		dev.ReadError = readDeviceError(dev.EventPath)
		devices = append(devices, dev)
	}
	slices.SortFunc(devices, func(a, b inputDeviceInfo) int {
		return strings.Compare(a.EventPath, b.EventPath)
	})
	return devices, nil
}

func parseProcInputBlock(block string) inputDeviceInfo {
	var dev inputDeviceInfo
	eventRe := regexp.MustCompile(`\bevent[0-9]+\b`)
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "I:"):
			parseIDLine(line, &dev)
		case strings.HasPrefix(line, "N:"):
			dev.Name = unquoteProcValue(strings.TrimSpace(strings.TrimPrefix(line, "N: Name=")))
		case strings.HasPrefix(line, "P:"):
			dev.Phys = strings.TrimSpace(strings.TrimPrefix(line, "P: Phys="))
		case strings.HasPrefix(line, "U:"):
			dev.Uniq = strings.TrimSpace(strings.TrimPrefix(line, "U: Uniq="))
		case strings.HasPrefix(line, "H:"):
			handlers := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "H: Handlers=")))
			dev.Handlers = handlers
			if event := eventRe.FindString(line); event != "" {
				dev.EventPath = "/dev/input/" + event
			}
		}
	}
	return dev
}

func parseIDLine(line string, dev *inputDeviceInfo) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "I:")))
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "Bus":
			dev.Bus = value
		case "Vendor":
			dev.Vendor = value
		case "Product":
			dev.Product = value
		case "Version":
			dev.Version = value
		}
	}
}

func unquoteProcValue(value string) string {
	value = strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return strings.Trim(value, `"`)
}

func cleanDevicePath(path string) string {
	if clean, err := filepath.Abs(path); err == nil {
		return clean
	}
	return path
}

func canReadDevice(path string) bool {
	return readDeviceError(path) == ""
}

func readDeviceError(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	_ = file.Close()
	return ""
}

func (d inputDeviceInfo) likelyGamepad() bool {
	if slices.Contains(d.Handlers, "js0") || slices.ContainsFunc(d.Handlers, func(h string) bool {
		return strings.HasPrefix(h, "js")
	}) {
		return true
	}
	name := strings.ToLower(d.Name)
	for _, token := range []string{"xbox", "controller", "gamepad", "joystick", "dualsense", "dualshock", "8bitdo", "steam deck"} {
		if strings.Contains(name, token) {
			return true
		}
	}
	for _, path := range d.StablePaths {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "event-joystick") || strings.Contains(lower, "gamepad") || strings.Contains(lower, "controller") {
			return true
		}
	}
	return false
}

func (d inputDeviceInfo) looksKeyboard() bool {
	if slices.Contains(d.Handlers, "kbd") {
		return true
	}
	name := strings.ToLower(d.Name)
	return strings.Contains(name, "keyboard")
}

func (d inputDeviceInfo) idSummary() string {
	var parts []string
	if d.Bus != "" {
		parts = append(parts, "bus="+d.Bus)
	}
	if d.Vendor != "" {
		parts = append(parts, "vendor="+d.Vendor)
	}
	if d.Product != "" {
		parts = append(parts, "product="+d.Product)
	}
	if d.Version != "" {
		parts = append(parts, "version="+d.Version)
	}
	if d.Uniq != "" {
		parts = append(parts, "uniq="+d.Uniq)
	}
	return strings.Join(parts, " ")
}

func compileChords(chords []Chord) ([]chordRuntime, error) {
	var out []chordRuntime
	for i, chord := range chords {
		if chord.Enabled != nil && !*chord.Enabled {
			continue
		}
		if chord.Name == "" {
			chord.Name = fmt.Sprintf("chord-%d", i+1)
		}
		inputs := chord.Chord
		if len(inputs) == 0 {
			inputs = chord.Inputs
		}
		codes, err := parseChord(inputs)
		if err != nil {
			return nil, fmt.Errorf("chord %q: %w", chord.Name, err)
		}
		if err := chord.Action.validate(); err != nil {
			return nil, fmt.Errorf("chord %q: %w", chord.Name, err)
		}
		out = append(out, chordRuntime{chord: chord, codes: codes})
	}
	return out, nil
}

func parseChord(names []string) ([]uint16, error) {
	var chord []uint16
	for _, part := range names {
		name := strings.ToUpper(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		code, ok := keyCodes[name]
		if !ok {
			return nil, fmt.Errorf("unknown evdev key name %q", name)
		}
		if !slices.Contains(chord, code) {
			chord = append(chord, code)
		}
	}
	if len(chord) == 0 {
		return nil, errors.New("chord must contain at least one key")
	}
	slices.Sort(chord)
	return chord, nil
}

func (a Action) validate() error {
	switch a.Type {
	case "exec":
		if len(a.Command) == 0 {
			return errors.New("exec action requires command")
		}
	case "shell":
		if strings.TrimSpace(a.Shell) == "" {
			return errors.New("shell action requires shell")
		}
	case "script":
		if strings.TrimSpace(a.Path) == "" {
			return errors.New("script action requires path")
		}
	case "systemd":
		if strings.TrimSpace(a.Unit) == "" {
			return errors.New("systemd action requires unit")
		}
	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
	return nil
}

func (a Action) describe() string {
	switch a.Type {
	case "exec":
		return strings.Join(a.Command, " ")
	case "shell":
		return "/bin/sh -c " + strconv.Quote(a.Shell)
	case "script":
		return strings.Join(append([]string{a.Path}, a.Args...), " ")
	case "systemd":
		op := a.Op
		if op == "" {
			op = "start"
		}
		scope := "system"
		if a.User {
			scope = "user"
		}
		return fmt.Sprintf("systemctl %s %s (%s)", op, a.Unit, scope)
	default:
		return a.Type
	}
}

func scanLoop(ctx context.Context, configuredDevices []string, allowAllDevices bool, chords []chordRuntime, scanInterval time.Duration, debug, verbose bool) error {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	watched := make(map[string]watchedDevice)
	for {
		if err := scanOnce(ctx, configuredDevices, allowAllDevices, watched, chords, debug, verbose); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			var wg sync.WaitGroup
			for _, dev := range watched {
				dev.cancel()
				wg.Add(1)
				go func(done <-chan struct{}) {
					defer wg.Done()
					<-done
				}(dev.done)
			}
			wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

func testInputLoop(ctx context.Context, configuredDevices []string, allowAllDevices bool, verbose bool) error {
	watched := make(map[string]watchedDevice)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		if err := testInputScanOnce(ctx, configuredDevices, allowAllDevices, watched, verbose); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			var wg sync.WaitGroup
			for _, dev := range watched {
				dev.cancel()
				wg.Add(1)
				go func(done <-chan struct{}) {
					defer wg.Done()
					<-done
				}(dev.done)
			}
			wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

func testInputScanOnce(parent context.Context, configuredDevices []string, allowAllDevices bool, watched map[string]watchedDevice, verbose bool) error {
	for path, dev := range watched {
		select {
		case <-dev.done:
			delete(watched, path)
		default:
		}
	}

	paths, err := devicePaths(configuredDevices, allowAllDevices)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, ok := watched[path]; ok {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			if os.IsPermission(err) {
				log.Printf("No permission to read %s", path)
			} else if verbose {
				log.Printf("Skipping %s: %v", path, err)
			}
			continue
		}

		ctx, cancel := context.WithCancel(parent)
		done := make(chan struct{})
		watched[path] = watchedDevice{cancel: cancel, done: done}
		go watchTestInputDevice(ctx, path, file, verbose, done)
	}
	return nil
}

func scanOnce(parent context.Context, configuredDevices []string, allowAllDevices bool, watched map[string]watchedDevice, chords []chordRuntime, debug, verbose bool) error {
	for path, dev := range watched {
		select {
		case <-dev.done:
			delete(watched, path)
		default:
		}
	}

	paths, err := devicePaths(configuredDevices, allowAllDevices)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, ok := watched[path]; ok {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			if os.IsPermission(err) {
				log.Printf("No permission to read %s", path)
			} else if verbose {
				log.Printf("Skipping %s: %v", path, err)
			}
			continue
		}

		ctx, cancel := context.WithCancel(parent)
		done := make(chan struct{})
		watched[path] = watchedDevice{cancel: cancel, done: done}
		deviceChords := cloneChords(chords)
		go watchDevice(ctx, path, file, deviceChords, debug, verbose, done)
	}
	return nil
}

func devicePaths(configured []string, allowAll bool) ([]string, error) {
	if len(configured) > 0 {
		var paths []string
		for _, rawPath := range configured {
			path := strings.TrimSpace(rawPath)
			if path != "" {
				paths = append(paths, path)
			}
		}
		return paths, nil
	}
	if !allowAll {
		return nil, errors.New("no devices configured; run 'chordio --list-devices' and add a controller path to config")
	}
	return filepath.Glob("/dev/input/event*")
}

func cloneChords(in []chordRuntime) []chordRuntime {
	out := make([]chordRuntime, len(in))
	copy(out, in)
	return out
}

func watchDevice(ctx context.Context, path string, file *os.File, chords []chordRuntime, debug, verbose bool, done chan<- struct{}) {
	defer close(done)
	defer file.Close()

	log.Printf("Watching %s", path)
	stopCloser := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = file.Close()
		case <-stopCloser:
		}
	}()
	defer close(stopCloser)

	pressed := make(map[uint16]bool)
	reader := bufio.NewReader(file)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		event, err := readInputEvent(reader)
		if err != nil {
			if err != io.EOF || verbose {
				log.Printf("Stopped watching %s: %v", path, err)
			}
			return
		}
		if event.Type != evKey {
			continue
		}

		if debug && (event.Value == 0 || event.Value == 1) {
			action := "released"
			if event.Value == 1 {
				action = "pressed"
			}
			log.Printf("%s %s on %s", keyName(event.Code), action, path)
		}

		switch event.Value {
		case 1:
			pressed[event.Code] = true
		case 0:
			delete(pressed, event.Code)
		default:
			continue
		}

		for i := range chords {
			handleChord(path, pressed, &chords[i], debug)
		}
	}
}

func watchTestInputDevice(ctx context.Context, path string, file *os.File, verbose bool, done chan<- struct{}) {
	defer close(done)
	defer file.Close()

	log.Printf("Watching %s", path)
	stopCloser := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = file.Close()
		case <-stopCloser:
		}
	}()
	defer close(stopCloser)

	reader := bufio.NewReader(file)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		event, err := readInputEvent(reader)
		if err != nil {
			if err != io.EOF || verbose {
				log.Printf("Stopped watching %s: %v", path, err)
			}
			return
		}
		if event.Type != evKey && event.Type != evAbs {
			continue
		}
		fmt.Printf("%s type=%s(%d) code=%s(%d) value=%d\n", path, eventTypeName(event.Type), event.Type, eventCodeName(event), event.Code, event.Value)
	}
}

func handleChord(path string, pressed map[uint16]bool, runtime *chordRuntime, debug bool) {
	isHeld := chordPressed(runtime.codes, pressed)
	if !isHeld {
		runtime.held = false
		return
	}
	if runtime.held {
		return
	}
	runtime.held = true

	cooldown := time.Duration(runtime.chord.CooldownMS) * time.Millisecond
	if cooldown > 0 && time.Since(runtime.lastFire) < cooldown {
		return
	}
	runtime.lastFire = time.Now()

	log.Printf("Chord %q held on %s: %s", runtime.chord.Name, path, formatChord(runtime.codes))
	if !debug {
		runAction(runtime.chord)
	}
}

func readInputEvent(r *bufio.Reader) (inputEvent, error) {
	var buf [24]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return inputEvent{}, err
	}
	return inputEvent{
		Type:  binary.LittleEndian.Uint16(buf[16:18]),
		Code:  binary.LittleEndian.Uint16(buf[18:20]),
		Value: int32(binary.LittleEndian.Uint32(buf[20:24])),
	}, nil
}

func chordPressed(chord []uint16, pressed map[uint16]bool) bool {
	for _, code := range chord {
		if !pressed[code] {
			return false
		}
	}
	return true
}

func runAction(chord Chord) {
	cmd, err := chord.Action.command()
	if err != nil {
		log.Printf("Chord %q invalid action: %v", chord.Name, err)
		return
	}
	log.Printf("Chord %q running: %s", chord.Name, strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		log.Printf("Chord %q failed to start: %v", chord.Name, err)
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("Chord %q exited with error: %v", chord.Name, err)
		}
	}()
}

func (a Action) command() (*exec.Cmd, error) {
	switch a.Type {
	case "exec":
		return exec.Command(a.Command[0], a.Command[1:]...), nil
	case "shell":
		return exec.Command("/bin/sh", "-c", a.Shell), nil
	case "script":
		args := append([]string{}, a.Args...)
		return exec.Command(a.Path, args...), nil
	case "systemd":
		op := a.Op
		if op == "" {
			op = "start"
		}
		args := []string{op, a.Unit}
		if a.User {
			args = append([]string{"--user"}, args...)
		}
		return exec.Command("systemctl", args...), nil
	default:
		return nil, fmt.Errorf("unknown action type %q", a.Type)
	}
}

func reverseKeyCodes() map[uint16]string {
	names := make(map[uint16]string)
	preferred := []string{
		"BTN_SOUTH", "BTN_EAST", "BTN_NORTH", "BTN_WEST",
		"BTN_TL", "BTN_TR", "BTN_TL2", "BTN_TR2",
		"BTN_SELECT", "BTN_START", "BTN_MODE", "BTN_THUMBL", "BTN_THUMBR",
	}
	for _, name := range preferred {
		names[keyCodes[name]] = name
	}
	for name, code := range keyCodes {
		if _, ok := names[code]; !ok {
			names[code] = name
		}
	}
	return names
}

func keyName(code uint16) string {
	if name, ok := codeNames[code]; ok {
		return name
	}
	return strconv.Itoa(int(code))
}

func eventTypeName(eventType uint16) string {
	if name, ok := eventTypeNames[eventType]; ok {
		return name
	}
	return strconv.Itoa(int(eventType))
}

func eventCodeName(event inputEvent) string {
	switch event.Type {
	case evKey:
		return keyName(event.Code)
	case evAbs:
		if name, ok := absCodeNames[event.Code]; ok {
			return name
		}
	}
	return strconv.Itoa(int(event.Code))
}

func formatChord(chord []uint16) string {
	var names []string
	for _, code := range chord {
		names = append(names, keyName(code))
	}
	return strings.Join(names, ", ")
}
