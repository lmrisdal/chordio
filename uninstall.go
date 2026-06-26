//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var systemdUnits = []string{"chordio.service"}

const (
	systemdDir     = "/etc/systemd/system"
	chordioConfDir = "/etc/chordio"
)

type unitState struct {
	name    string
	path    string
	enabled bool
	active  bool
}

// cmdUninstall is the interactive uninstaller. It shows what's installed, lets
// the user pick which systemd services to remove, and offers a final
// "everything" option that also deletes the binary and config.
func cmdUninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("uninstall needs root; re-run: sudo chordio uninstall")
	}

	units, err := installedUnits()
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	if s, e := filepath.EvalSymlinks(self); e == nil {
		self = s
	}
	haveConfig := dirExists(chordioConfDir)

	if len(units) == 0 && !haveConfig {
		fmt.Printf("Nothing to uninstall (no services or config found).\nRemove the binary manually with: rm %s\n", self)
		return nil
	}

	fmt.Println("Installed chordio components:")
	if len(units) > 0 {
		for _, u := range units {
			state := "disabled"
			if u.enabled {
				state = "enabled"
			}
			if u.active {
				state += ", active"
			}
			fmt.Printf("  - %s (%s)\n", u.name, state)
		}
	} else {
		fmt.Println("  - no systemd services found")
	}
	fmt.Printf("  - binary: %s\n", self)
	if haveConfig {
		fmt.Printf("  - config: %s\n", chordioConfDir)
	}
	fmt.Println()

	everythingIdx := len(units) + 1
	fmt.Println("Select what to uninstall:")
	for i, u := range units {
		fmt.Printf("  %d) %s\n", i+1, u.name)
	}
	if haveConfig {
		fmt.Printf("  %d) Everything (services + binary + config)\n", everythingIdx)
	} else {
		fmt.Printf("  %d) Everything (services + binary)\n", everythingIdx)
	}

	r := bufio.NewReader(os.Stdin)
	sel, err := prompt(r, "Enter numbers (space/comma separated) or 'q' to cancel: ")
	if err != nil || sel == "" || strings.EqualFold(sel, "q") {
		fmt.Println("Cancelled.")
		return nil
	}

	picks, err := parseSelection(sel, everythingIdx)
	if err != nil {
		return err
	}

	var (
		toRemove   []unitState
		doBinary   bool
		doConfig   bool
		everything bool
	)
	if picks[everythingIdx] {
		everything = true
		toRemove = units
		doBinary = true
		doConfig = haveConfig
	} else {
		for i, u := range units {
			if picks[i+1] {
				toRemove = append(toRemove, u)
			}
		}
	}

	if len(toRemove) == 0 && !doBinary && !doConfig {
		fmt.Println("Nothing selected; cancelled.")
		return nil
	}

	fmt.Println("\nAbout to remove:")
	for _, u := range toRemove {
		fmt.Printf("  - %s\n", u.name)
	}
	if doBinary {
		fmt.Printf("  - the chordio binary (%s)\n", self)
	}
	if doConfig {
		fmt.Printf("  - the config (%s)\n", chordioConfDir)
	}
	ans, _ := prompt(r, "Proceed? [y/N] ")
	if !strings.HasPrefix(strings.ToLower(ans), "y") {
		fmt.Println("Aborted.")
		return nil
	}

	for _, u := range toRemove {
		if err := removeUnit(u); err != nil {
			return err
		}
	}
	if len(toRemove) > 0 {
		if err := runSystemctl("daemon-reload"); err != nil {
			return err
		}
		_ = runSystemctl("reset-failed")
	}

	if doConfig {
		logf("removing %s", chordioConfDir)
		if err := os.RemoveAll(chordioConfDir); err != nil {
			return err
		}
	}
	if doBinary {
		logf("removing %s", self)
		if err := os.Remove(self); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	if everything {
		fmt.Println("\nchordio has been fully uninstalled.")
	} else {
		fmt.Printf("\nRemoved %d systemd service(s). The binary and config were left in place.\n", len(toRemove))
	}
	return nil
}

func installedUnits() ([]unitState, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, nil
	}
	var out []unitState
	for _, u := range systemdUnits {
		path := filepath.Join(systemdDir, u)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, unitState{name: u, path: path, enabled: unitEnabled(u), active: unitActive(u)})
	}
	return out, nil
}

func unitEnabled(u string) bool {
	out, _ := exec.Command("systemctl", "is-enabled", u).Output()
	return strings.TrimSpace(string(out)) == "enabled"
}

func unitActive(u string) bool {
	out, _ := exec.Command("systemctl", "is-active", u).Output()
	return strings.TrimSpace(string(out)) == "active"
}

func removeUnit(u unitState) error {
	if u.active {
		logf("stopping %s", u.name)
		if err := runSystemctl("stop", u.name); err != nil {
			return err
		}
	}
	logf("disabling %s", u.name)
	if err := runSystemctl("disable", u.name); err != nil {
		return err
	}
	logf("removing %s", u.path)
	if err := os.Remove(u.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", args[0], err)
	}
	return nil
}

func parseSelection(s string, max int) (map[int]bool, error) {
	picks := make(map[int]bool)
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > max {
			return nil, fmt.Errorf("invalid selection %q (choose 1-%d)", f, max)
		}
		picks[n] = true
	}
	return picks, nil
}

func prompt(r *bufio.Reader, q string) (string, error) {
	fmt.Print(q)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
