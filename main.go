package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sensu-community/sensu-plugin-sdk/sensu"
	"github.com/sensu/sensu-go/types"
	"github.com/shirou/gopsutil/v3/cpu"
)

// Config represents the check plugin config.
type Config struct {
	sensu.PluginConfig
	Critical float64
	Warning  float64
	Interval int
}

// ProcessInfo holds information about a single process
type ProcessInfo struct {
	PID  int
	CPU  float64
	Name string
}

var (
	plugin = Config{
		PluginConfig: sensu.PluginConfig{
			Name:     "cpu-process-profiler",
			Short:    "Check CPU usage and provide metrics",
			Keyspace: "sensu.io/plugins/cpu-process-profiler/config",
		},
	}

	options = []*sensu.PluginConfigOption{
		{
			Path:      "critical",
			Argument:  "critical",
			Shorthand: "c",
			Default:   float64(90),
			Usage:     "Critical threshold for overall CPU usage",
			Value:     &plugin.Critical,
		},
		{
			Path:      "warning",
			Argument:  "warning",
			Shorthand: "w",
			Default:   float64(75),
			Usage:     "Warning threshold for overall CPU usage",
			Value:     &plugin.Warning,
		},
		{
			Path:      "sample-interval",
			Argument:  "sample-interval",
			Shorthand: "s",
			Default:   2,
			Usage:     "Length of sample interval in seconds",
			Value:     &plugin.Interval,
		},
	}
)

func main() {
	check := sensu.NewGoCheck(&plugin.PluginConfig, options, checkArgs, executeCheck, false)
	check.Execute()
}

func checkArgs(event *types.Event) (int, error) {
	if plugin.Critical == 0 {
		return sensu.CheckStateWarning, fmt.Errorf("--critical is required")
	}
	if plugin.Warning == 0 {
		return sensu.CheckStateWarning, fmt.Errorf("--warning is required")
	}
	if plugin.Warning > plugin.Critical {
		return sensu.CheckStateWarning, fmt.Errorf("--warning cannot be greater than --critical")
	}
	if plugin.Interval == 0 {
		return sensu.CheckStateWarning, fmt.Errorf("--interval is required")
	}
	return sensu.CheckStateOK, nil
}

func executeCheck(event *types.Event) (int, error) {
	start, err := cpu.Times(false)
	if err != nil {
		return sensu.CheckStateCritical, fmt.Errorf("error obtaining CPU timings: %v", err)
	}

	startTotal := start[0].User + start[0].System + start[0].Idle + start[0].Nice + start[0].Iowait + start[0].Irq + start[0].Softirq + start[0].Steal + start[0].Guest + start[0].GuestNice

	duration, err := time.ParseDuration(fmt.Sprintf("%ds", plugin.Interval))
	if err != nil {
		return sensu.CheckStateCritical, fmt.Errorf("error parsing duration: %v", err)
	}

	time.Sleep(duration)

	end, err := cpu.Times(false)
	if err != nil {
		return sensu.CheckStateCritical, fmt.Errorf("error obtaining CPU timings: %v", err)
	}

	endTotal := end[0].User + end[0].System + end[0].Idle + end[0].Nice + end[0].Iowait + end[0].Irq + end[0].Softirq + end[0].Steal + end[0].Guest + end[0].GuestNice

	diff := endTotal - startTotal
	idlePct := ((end[0].Idle - start[0].Idle) / diff) * 100
	usedPct := 100 - idlePct

	userPct := ((end[0].User - start[0].User) / diff) * 100
	sysPct := ((end[0].System - start[0].System) / diff) * 100
	nicePct := ((end[0].Nice - start[0].Nice) / diff) * 100
	iowaitPct := ((end[0].Iowait - start[0].Iowait) / diff) * 100
	irqPct := ((end[0].Irq - start[0].Irq) / diff) * 100
	softirqPct := ((end[0].Softirq - start[0].Softirq) / diff) * 100
	stealPct := ((end[0].Steal - start[0].Steal) / diff) * 100
	guestPct := ((end[0].Guest - start[0].Guest) / diff) * 100
	guestnicePct := ((end[0].GuestNice - start[0].GuestNice) / diff) * 100
	perfData := fmt.Sprintf("cpu_idle=%.2f, cpu_system=%.2f, cpu_user=%.2f, cpu_nice=%.2f, cpu_iowait=%.2f, cpu_irq=%.2f, cpu_softirq=%.2f, cpu_steal=%.2f, cpu_guest=%.2f, cpu_guestnice=%.2f", idlePct, sysPct, userPct, nicePct, iowaitPct, irqPct, softirqPct, stealPct, guestPct, guestnicePct)

	topProcesses, err := getTopCPUProcesses()
	if err != nil {
		return sensu.CheckStateCritical, fmt.Errorf("error obtaining top CPU processes: %v", err)
	}

	processInfo := "\nTop CPU processes:\n"
	for _, p := range topProcesses {
		processInfo += fmt.Sprintf("PID %d (%s): %.2f%%\n", p.PID, p.Name, p.CPU)
	}

	if usedPct > plugin.Critical {
		fmt.Printf("%s Critical: %.2f%% CPU usage | %s\n%s\n", plugin.PluginConfig.Name, usedPct, perfData, processInfo)
		return sensu.CheckStateCritical, nil
	} else if usedPct > plugin.Warning {
		fmt.Printf("%s Warning: %.2f%% CPU usage | %s\n%s\n", plugin.PluginConfig.Name, usedPct, perfData, processInfo)
		return sensu.CheckStateWarning, nil
	}

	fmt.Printf("%s OK: %.2f%% CPU usage | %s\n%s\n", plugin.PluginConfig.Name, usedPct, perfData, processInfo)
	return sensu.CheckStateOK, nil
}

func getTopCPUProcesses() ([]ProcessInfo, error) {
	var cmd *exec.Cmd
	var output []byte
	var err error

	switch runtime.GOOS {
	case "darwin", "linux":
		cmd = exec.Command("ps", "aux", "--sort=-pcpu")
		output, err = cmd.Output()
	case "windows":
		cmd = exec.Command("tasklist", "/v", "/fo", "csv", "/nh", "/sort:cpu")
		output, err = cmd.Output()
	default:
		return nil, fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	if err != nil {
		return nil, fmt.Errorf("error executing command: %v", err)
	}

	return parseProcessOutput(string(output), runtime.GOOS)
}

func parseProcessOutput(output, os string) ([]ProcessInfo, error) {
	lines := strings.Split(output, "\n")
	var processes []ProcessInfo

	switch os {
	case "darwin", "linux":
		for _, line := range lines[1:] { // Skip header
			fields := strings.Fields(line)
			if len(fields) < 11 {
				continue
			}
			cpu, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			processes = append(processes, ProcessInfo{
				PID:  pid,
				CPU:  cpu,
				Name: fields[10],
			})
		}
	case "windows":
		for _, line := range lines {
			fields := strings.Split(line, ",")
			if len(fields) < 8 {
				continue
			}
			cpu := strings.Trim(fields[7], "\"")
			cpuFloat, err := strconv.ParseFloat(strings.TrimSuffix(cpu, " K"), 64)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.Trim(fields[1], "\""))
			if err != nil {
				continue
			}
			processes = append(processes, ProcessInfo{
				PID:  pid,
				CPU:  cpuFloat,
				Name: strings.Trim(fields[0], "\""),
			})
		}
	}

	sort.Slice(processes, func(i, j int) bool {
		return processes[i].CPU > processes[j].CPU
	})

	if len(processes) > 10 {
		processes = processes[:10]
	}

	return processes, nil
}
