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

// FormatCPULoad formats a raw CPU load reading as a percentage.
func FormatCPULoad(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("cpu_load: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("cpu_load: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("cpu_load: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("cpu_load=%.1f%%", v), nil
}

// FormatMemUsed formats a raw memory usage reading as a percentage.
func FormatMemUsed(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("mem_used: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("mem_used: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("mem_used: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("mem_used=%.1f%%", v), nil
}

// FormatDiskUsed formats a raw disk usage reading as a percentage.
func FormatDiskUsed(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("disk_used: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("disk_used: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("disk_used: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("disk_used=%.1f%%", v), nil
}

// FormatNetRx formats a raw network receive rate in Mbps.
func FormatNetRx(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("net_rx: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("net_rx: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("net_rx: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 10000 {
		v = 10000
	}
	return fmt.Sprintf("net_rx=%.0fmbps", v), nil
}

// FormatNetTx formats a raw network transmit rate in Mbps.
func FormatNetTx(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("net_tx: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("net_tx: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("net_tx: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 10000 {
		v = 10000
	}
	return fmt.Sprintf("net_tx=%.0fmbps", v), nil
}

// FormatGPUUtil formats a raw GPU utilization reading as a percentage.
func FormatGPUUtil(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("gpu_util: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("gpu_util: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("gpu_util: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("gpu_util=%.1f%%", v), nil
}

// FormatGPUMem formats a raw GPU memory usage reading as a percentage.
func FormatGPUMem(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("gpu_mem: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("gpu_mem: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("gpu_mem: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("gpu_mem=%.1f%%", v), nil
}

// FormatTempCPU formats a raw CPU temperature reading in Celsius.
func FormatTempCPU(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("temp_cpu: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("temp_cpu: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("temp_cpu: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 120 {
		v = 120
	}
	return fmt.Sprintf("temp_cpu=%.1fc", v), nil
}

// FormatTempGPU formats a raw GPU temperature reading in Celsius.
func FormatTempGPU(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("temp_gpu: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("temp_gpu: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("temp_gpu: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 120 {
		v = 120
	}
	return fmt.Sprintf("temp_gpu=%.1fc", v), nil
}

// FormatFanSpeed formats a raw fan speed reading in RPM.
func FormatFanSpeed(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("fan_speed: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("fan_speed: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("fan_speed: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 10000 {
		v = 10000
	}
	return fmt.Sprintf("fan_speed=%.0frpm", v), nil
}

// FormatPowerDraw formats a raw power draw reading in watts.
func FormatPowerDraw(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("power_draw: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("power_draw: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("power_draw: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 2000 {
		v = 2000
	}
	return fmt.Sprintf("power_draw=%.0fw", v), nil
}

// FormatUptimeDays formats a raw uptime reading in days.
func FormatUptimeDays(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("uptime_days: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("uptime_days: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("uptime_days: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 10000 {
		v = 10000
	}
	return fmt.Sprintf("uptime_days=%.0fd", v), nil
}

// FormatLoadAvg formats a raw load average reading.
func FormatLoadAvg(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("load_avg: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("load_avg: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("load_avg: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 1024 {
		v = 1024
	}
	return fmt.Sprintf("load_avg=%.2f", v), nil
}

// FormatSwapUsed formats a raw swap usage reading as a percentage.
func FormatSwapUsed(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", errors.New("swap_used: empty input")
	}
	if strings.HasPrefix(s, "err:") {
		return "", fmt.Errorf("swap_used: upstream error %q", strings.TrimPrefix(s, "err:"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("swap_used: parse: %w", err)
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("swap_used=%.1f%%", v), nil
}
