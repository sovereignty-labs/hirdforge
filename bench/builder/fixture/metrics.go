// Package metrics formats raw collector readings for the fleet dashboard.
//
// BENCHMARK FIXTURE — this file deliberately contains the same
// validate/parse/clamp/format sequence duplicated across 14 functions. The
// benchmark task is to extract the shared pure helper without changing any
// public signature or behavior. See bench/builder/README.md.
package metrics

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// formatMetric validates, parses, clamps, and formats a raw metric reading.
func formatMetric(name, raw string, max float64, format string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New(name + ": empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf(name+": upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf(name+": parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > max {
		v = max
	}
	return fmt.Sprintf(format, v), nil
}

// FormatCPULoad formats a raw CPU load reading as a percentage.
func FormatCPULoad(raw string) (string, error) {
	return formatMetric("cpu_load", raw, 100, "cpu_load=%.1f%%")
}

// FormatMemUsed formats a raw memory usage reading as a percentage.
func FormatMemUsed(raw string) (string, error) {
	return formatMetric("mem_used", raw, 100, "mem_used=%.1f%%")
}

// FormatDiskUsed formats a raw disk usage reading as a percentage.
func FormatDiskUsed(raw string) (string, error) {
	return formatMetric("disk_used", raw, 100, "disk_used=%.1f%%")
}

// FormatNetRx formats a raw network receive rate in Mbps.
func FormatNetRx(raw string) (string, error) {
	return formatMetric("net_rx", raw, 10000, "net_rx=%.0fmbps")
}

// FormatNetTx formats a raw network transmit rate in Mbps.
func FormatNetTx(raw string) (string, error) {
	return formatMetric("net_tx", raw, 10000, "net_tx=%.0fmbps")
}

// FormatGPUUtil formats a raw GPU utilization reading as a percentage.
func FormatGPUUtil(raw string) (string, error) {
	return formatMetric("gpu_util", raw, 100, "gpu_util=%.1f%%")
}

// FormatGPUMem formats a raw GPU memory usage reading as a percentage.
func FormatGPUMem(raw string) (string, error) {
	return formatMetric("gpu_mem", raw, 100, "gpu_mem=%.1f%%")
}

// FormatTempCPU formats a raw CPU temperature reading in Celsius.
func FormatTempCPU(raw string) (string, error) {
	return formatMetric("temp_cpu", raw, 120, "temp_cpu=%.1fc")
}

// FormatTempGPU formats a raw GPU temperature reading in Celsius.
func FormatTempGPU(raw string) (string, error) {
	return formatMetric("temp_gpu", raw, 120, "temp_gpu=%.1fc")
}

// FormatFanSpeed formats a raw fan speed reading in RPM.
func FormatFanSpeed(raw string) (string, error) {
	return formatMetric("fan_speed", raw, 10000, "fan_speed=%.0frpm")
}

// FormatPowerDraw formats a raw power draw reading in watts.
func FormatPowerDraw(raw string) (string, error) {
	return formatMetric("power_draw", raw, 2000, "power_draw=%.0fw")
}

// FormatUptimeDays formats a raw uptime reading in days.
func FormatUptimeDays(raw string) (string, error) {
	return formatMetric("uptime_days", raw, 10000, "uptime_days=%.0fd")
}

// FormatLoadAvg formats a raw load average reading.
func FormatLoadAvg(raw string) (string, error) {
	return formatMetric("load_avg", raw, 1024, "load_avg=%.2f")
}

// FormatSwapUsed formats a raw swap usage reading as a percentage.
func FormatSwapUsed(raw string) (string, error) {
	return formatMetric("swap_used", raw, 100, "swap_used=%.1f%%")
}
