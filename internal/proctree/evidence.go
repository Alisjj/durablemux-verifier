package proctree

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateText checks that a snapshot contains all required process fields and
// an observable parent/child relationship, including a live sleep 30 command.
func ValidateText(data []byte) error {
	columns := map[string]int{}
	parents := map[int]int{}
	commands := map[int]bool{}
	shells := map[int]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "[exit]" && len(fields) == 2 && fields[1] != "0" {
			return fmt.Errorf("stage 4 needs a successful capture, not a failed command transcript")
		}
		if fields[0] == "[timeout]" && len(fields) == 2 && fields[1] != "false" {
			return fmt.Errorf("stage 4 process capture timed out; press t to capture the experiment")
		}
		if strings.HasPrefix(line, "[runner error] ") && strings.TrimSpace(strings.TrimPrefix(line, "[runner error] ")) != "" {
			return fmt.Errorf("stage 4 process capture has a runner error; press t to capture the experiment")
		}
		if len(fields) > 1 && fields[0] == "PID" && fields[1] == "PPID" {
			columns = map[string]int{}
			parents, commands, shells = map[int]int{}, map[int]bool{}, map[int]bool{}
			for i, field := range fields {
				columns[field] = i
			}
			for _, field := range []string{"PID", "PPID", "PGID", "SID", "TPGID", "STAT", "TTY"} {
				if _, ok := columns[field]; !ok {
					return fmt.Errorf("process snapshot is missing %s", field)
				}
			}
			if _, ok := columns["COMMAND"]; !ok {
				if _, ok := columns["CMD"]; !ok {
					if _, ok := columns["ARGS"]; !ok {
						return fmt.Errorf("process snapshot is missing the command column")
					}
				}
			}
			continue
		}
		if len(columns) == 0 {
			continue
		}
		valid := true
		values := map[string]int{}
		for _, field := range []string{"PID", "PPID", "PGID", "SID", "TPGID"} {
			i := columns[field]
			if i >= len(fields) {
				valid = false
				break
			}
			n, err := strconv.Atoi(fields[i])
			if err != nil {
				valid = false
				break
			}
			values[field] = n
		}
		if !valid || values["PID"] <= 0 || values["PGID"] <= 0 || values["SID"] <= 0 || columns["STAT"] >= len(fields) || columns["TTY"] >= len(fields) {
			continue
		}
		if _, exists := parents[values["PID"]]; exists {
			return fmt.Errorf("process snapshot contains duplicate or contradictory rows for PID %d", values["PID"])
		}
		if strings.HasPrefix(fields[columns["STAT"]], "Z") {
			continue
		}
		parents[values["PID"]] = values["PPID"]
		for _, field := range []string{"COMMAND", "CMD", "ARGS"} {
			if i, ok := columns[field]; ok && i < len(fields) {
				name := strings.TrimPrefix(filepath.Base(fields[i]), "-")
				if i+2 == len(fields) && name == "sleep" && fields[i+1] == "30" {
					commands[values["PID"]] = true
				}
				switch name {
				case "bash", "sh", "zsh", "dash", "ksh", "mksh", "csh", "tcsh", "fish", "nu", "xonsh", "elvish":
					shells[values["PID"]] = true
				}
			}
		}
	}
	for pid := range commands {
		seen := map[int]bool{pid: true}
		for parent, ok := parents[pid]; ok && !seen[parent]; parent, ok = parents[parent] {
			if shells[parent] {
				return nil
			}
			seen[parent] = true
		}
	}
	return fmt.Errorf("stage 4 needs a process snapshot with PID, PPID, PGID, SID, TPGID, STAT, TTY, command, and the invoking shell's live sleep 30 process tree; press t to capture the experiment")
}
