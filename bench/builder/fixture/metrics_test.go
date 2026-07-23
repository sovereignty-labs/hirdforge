package metrics

import "testing"

// The behavior contract: every function validates, clamps, and formats the
// same way, differing only in name, clamp range, unit, and precision. The
// benchmark forbids editing this file — the refactor must keep it green.

func TestFormatFunctions(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(string) (string, error)
		in      string
		want    string
		wantErr bool
	}{
		{"cpu_load ok", FormatCPULoad, " 42.5 ", "cpu_load=42.5%", false},
		{"cpu_load clamps high", FormatCPULoad, "250", "cpu_load=100.0%", false},
		{"cpu_load clamps low", FormatCPULoad, "-3", "cpu_load=0.0%", false},
		{"cpu_load empty", FormatCPULoad, "   ", "", true},
		{"cpu_load upstream err", FormatCPULoad, "ERR:sensor down", "", true},
		{"cpu_load junk", FormatCPULoad, "abc", "", true},

		{"mem_used ok", FormatMemUsed, "77.25", "mem_used=77.2%", false},
		{"mem_used upstream err", FormatMemUsed, "err:oom", "", true},

		{"disk_used ok", FormatDiskUsed, "88.8", "disk_used=88.8%", false},
		{"disk_used clamps high", FormatDiskUsed, "101", "disk_used=100.0%", false},

		{"net_rx ok", FormatNetRx, "941.7", "net_rx=942mbps", false},
		{"net_rx clamps high", FormatNetRx, "20000", "net_rx=10000mbps", false},

		{"net_tx ok", FormatNetTx, "10.2", "net_tx=10mbps", false},
		{"net_tx empty", FormatNetTx, "", "", true},

		{"gpu_util ok", FormatGPUUtil, "99.9", "gpu_util=99.9%", false},
		{"gpu_util junk", FormatGPUUtil, "n/a", "", true},

		{"gpu_mem ok", FormatGPUMem, "64", "gpu_mem=64.0%", false},

		{"temp_cpu ok", FormatTempCPU, "71.3", "temp_cpu=71.3c", false},
		{"temp_cpu clamps high", FormatTempCPU, "300", "temp_cpu=120.0c", false},

		{"temp_gpu ok", FormatTempGPU, " 83 ", "temp_gpu=83.0c", false},

		{"fan_speed ok", FormatFanSpeed, "2450.6", "fan_speed=2451rpm", false},
		{"fan_speed clamps low", FormatFanSpeed, "-1", "fan_speed=0rpm", false},

		{"power_draw ok", FormatPowerDraw, "455", "power_draw=455w", false},
		{"power_draw clamps high", FormatPowerDraw, "9999", "power_draw=2000w", false},

		{"uptime_days ok", FormatUptimeDays, "365.9", "uptime_days=366d", false},

		{"load_avg ok", FormatLoadAvg, "12.345", "load_avg=12.35", false},
		{"load_avg clamps high", FormatLoadAvg, "2048", "load_avg=1024.00", false},

		{"swap_used ok", FormatSwapUsed, "0", "swap_used=0.0%", false},
		{"swap_used upstream err", FormatSwapUsed, "err:disabled", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
