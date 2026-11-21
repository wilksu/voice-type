package kwin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const ruleID = "voice-type-hud"

var modeRe = regexp.MustCompile(`(\d+)x(\d+)@`)

func RulesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "kwinrulesrc")
}

func detectScreenSize() (int, int) {
	out, err := exec.Command("kscreen-doctor", "-o").Output()
	if err == nil {
		if m := modeRe.FindStringSubmatch(string(out)); len(m) == 3 {
			w, err1 := strconv.Atoi(m[1])
			h, err2 := strconv.Atoi(m[2])
			if err1 == nil && err2 == nil && w > 0 && h > 0 {
				return w, h
			}
		}
	}
	return 1920, 1080
}

// InstallRule writes/updates the [voice-type-hud] KWin rule in ~/.config/kwinrulesrc
// and tells KWin to reconfigure via D-Bus.
func InstallRule(bottomPercent float64) (string, error) {
	if bottomPercent < 0 {
		bottomPercent = 0
	}
	if bottomPercent > 50 {
		bottomPercent = 50
	}

	w, h := detectScreenSize()
	pillWidth := 160
	x := (w - pillWidth) / 2
	if x < 0 {
		x = 0
	}
	y := int(float64(h) * (1.0 - bottomPercent/100.0))
	if y < 0 {
		y = 0
	}

	path := RulesPath()
	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}

	updated := upsertRuleINI(existing, x, y)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", err
	}

	if conn, err := dbus.SessionBus(); err == nil {
		_ = conn.Object("org.kde.KWin", "/KWin").Call("org.kde.KWin.reconfigure", 0).Err
	}
	return fmt.Sprintf("%d,%d", x, y), nil
}

func upsertRuleINI(content string, x, y int) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	sections := make(map[string][]string)
	var order []string
	curSection := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			curSection = trimmed[1 : len(trimmed)-1]
			if _, ok := sections[curSection]; !ok {
				order = append(order, curSection)
			}
			continue
		}
		if curSection != "" && trimmed != "" {
			sections[curSection] = append(sections[curSection], line)
		}
	}

	// Update [General] rules & count
	var existingRules []string
	var otherGeneral []string
	for _, l := range sections["General"] {
		if strings.HasPrefix(l, "rules=") {
			raw := strings.TrimPrefix(l, "rules=")
			for _, r := range strings.Split(raw, ",") {
				r = strings.TrimSpace(r)
				if r != "" {
					existingRules = append(existingRules, r)
				}
			}
		} else if !strings.HasPrefix(l, "count=") {
			otherGeneral = append(otherGeneral, l)
		}
	}
	found := false
	for _, r := range existingRules {
		if r == ruleID {
			found = true
			break
		}
	}
	if !found {
		existingRules = append(existingRules, ruleID)
	}

	generalLines := append([]string{
		fmt.Sprintf("count=%d", len(existingRules)),
		"rules=" + strings.Join(existingRules, ","),
	}, otherGeneral...)
	sections["General"] = generalLines

	if !contains(order, "General") {
		order = append([]string{"General"}, order...)
	}
	if !contains(order, ruleID) {
		order = append(order, ruleID)
	}

	sections[ruleID] = []string{
		"Description=voice-type HUD",
		"wmclass=io.github.voicetype.hud",
		"wmclassmatch=1",
		"above=true",
		"aboverule=2",
		"acceptfocus=false",
		"acceptfocusrule=2",
		"noborder=true",
		"noborderrule=2",
		"skiptaskbar=true",
		"skiptaskbarrule=2",
		"skippager=true",
		"skippagerrule=2",
		"skipswitcher=true",
		"skipswitcherrule=2",
		fmt.Sprintf("position=%d,%d", x, y),
		"positionrule=2",
	}

	var out []string
	for _, sec := range order {
		out = append(out, "["+sec+"]")
		out = append(out, sections[sec]...)
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
