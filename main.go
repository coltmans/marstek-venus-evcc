// marstek-api: HTTP proxy bridging the Marstek local UDP API to evcc.
//
// Endpoints:
//   GET  /api/soc         → {"soc": 85.0}         (%, averaged across all devices)
//   GET  /api/power       → {"power": 1234.0}      (W, positive = charging)
//   GET  /api/status      → full aggregated status
//   POST /api/batterymode → set Normal / Charge / Discharge
//
// Usage:
//   MARSTEK_DEVICES=192.168.1.100,192.168.1.101 ./marstek-api
//   or just run with the built-in defaults below.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"marstek-api/marstek"
)

// ---- configuration --------------------------------------------------------

type deviceConfig struct {
	Host string
	Port int
}

// defaultDevices is used when MARSTEK_DEVICES env var is not set.
// Override at runtime: MARSTEK_DEVICES=192.168.1.100,192.168.1.101 ./marstek-api
var defaultDevices = []deviceConfig{
	{Host: "192.168.1.100", Port: 30000},
}

const (
	udpTimeout = 3 * time.Second
	listenAddr = ":7071"

	// passiveCountdown is how long (seconds) the Marstek holds a passive-mode
	// power setpoint before reverting to auto. evcc polls every ~30 s so 90 s
	// gives two missed polls before the battery falls back to auto mode.
	passiveCountdown = 90

	// invertBatPower: evcc battery convention is positive=discharging, negative=charging.
	// Marstek bat_power is positive while charging, so we invert.
	invertBatPower = true

	// maxBatPowerPerDevice: sanity limit per device — readings outside ±this
	// value are treated as garbage and ignored. Venus E V2 max is ~2500W.
	maxBatPowerPerDevice = 6000.0

	// minSOC: refuse discharge commands when the averaged SOC is at or below
	// this value. Matches the DOD setting in the Marstek app (default 11%).
	minSOC = 11.0

	// defaultPowerPerDevice: per-device power used when evcc doesn't send a
	// specific wattage. Venus E max is ~2500 W per unit.
	defaultPowerPerDevice = 2500
)

// ---------------------------------------------------------------------------

type client struct {
	mc   *marstek.Client
	name string
}

func devicesFromEnv() []deviceConfig {
	env := os.Getenv("MARSTEK_DEVICES")
	if env == "" {
		return defaultDevices
	}
	port := 30000
	if p := os.Getenv("MARSTEK_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	var cfg []deviceConfig
	for _, h := range strings.Split(env, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			cfg = append(cfg, deviceConfig{Host: h, Port: port})
		}
	}
	return cfg
}

// aggregatedStatus holds the combined view across all batteries.
type aggregatedStatus struct {
	SOC         float64 `json:"soc"`          // %, capacity-weighted average
	Power       float64 `json:"power"`         // W, summed; positive = charging
	PVPower     float64 `json:"pv_power"`      // W, summed
	OnGridPower float64 `json:"ongrid_power"`  // W, summed
	TotalCapWh  float64 `json:"total_cap_wh"`  // Wh
	Devices     int     `json:"devices"`
}

func aggregate(clients []client) (*aggregatedStatus, error) {
	var (
		weightedSOC float64
		totalCap    float64
		sumPower    float64
		sumPV       float64
		sumOnGrid   float64
		ok          int
	)

	for _, c := range clients {
		st, err := c.mc.GetESStatus()
		if err != nil {
			log.Printf("WARN: %s GetESStatus: %v", c.name, err)
			continue
		}
		ok++

		cap := 5120.0 // fallback rated capacity per Venus E (Wh)
		if st.BatCap != nil && *st.BatCap > 0 {
			cap = *st.BatCap
		}

		if st.BatSOC != nil {
			weightedSOC += *st.BatSOC * cap
			totalCap += cap
		}

		if st.BatPower != nil {
			p := *st.BatPower
			if p > maxBatPowerPerDevice || p < -maxBatPowerPerDevice {
				// bat_power out of range — fall back to -ongrid_power.
				// Venus E has no PV input so bat_power ≈ -ongrid_power.
				if st.OnGridPower != nil {
					p = -*st.OnGridPower
					log.Printf("WARN: %s bat_power=%.0f out of range, using -ongrid_power=%.0f", c.name, *st.BatPower, p)
				} else {
					log.Printf("WARN: %s bat_power=%.0f out of range, no fallback", c.name, p)
					p = 0
				}
			}
			if invertBatPower {
				p = -p
			}
			sumPower += p
		}
		if st.PVPower != nil {
			sumPV += *st.PVPower
		}
		if st.OnGridPower != nil {
			sumOnGrid += *st.OnGridPower
		}
	}

	if ok == 0 {
		return nil, fmt.Errorf("no devices responded")
	}

	soc := 0.0
	if totalCap > 0 {
		soc = weightedSOC / totalCap
	}

	return &aggregatedStatus{
		SOC:         soc,
		Power:       sumPower,
		PVPower:     sumPV,
		OnGridPower: sumOnGrid,
		TotalCapWh:  totalCap,
		Devices:     ok,
	}, nil
}

// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handleSOC(clients []client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := aggregate(clients)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, map[string]float64{"soc": st.SOC})
	}
}

func handlePower(clients []client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := aggregate(clients)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, map[string]float64{"power": st.Power})
	}
}

func handleStatus(clients []client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := aggregate(clients)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, st)
	}
}

// BatteryModeRequest is what evcc sends to /api/batterymode.
// mode: "normal" | "charge" | "discharge"
// power: requested power in W (optional, used for passive mode)
type BatteryModeRequest struct {
	Mode  string  `json:"mode"`
	Power float64 `json:"power"`
}

// handleEM returns the house-only power from the Marstek energy meter (CT clamp).
// Uses the first device that responds — both batteries share the same EM.
func handleEM(clients []client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, c := range clients {
			st, err := c.mc.GetEMStatus()
			if err != nil {
				continue
			}
			power := 0.0
			if st.TotalPower != nil {
				power = *st.TotalPower
			}
			writeJSON(w, map[string]any{
				"power":         power,
				"a_power":       st.APower,
				"b_power":       st.BPower,
				"c_power":       st.CPower,
				"input_energy":  st.InputEnergy,
				"output_energy": st.OutputEnergy,
			})
			return
		}
		writeError(w, http.StatusBadGateway, "no devices responded")
	}
}

func handleBatteryMode(clients []client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}

		var req BatteryModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		// Read current SOC before acting so we can enforce the minimum.
		currentSOC := 100.0 // safe default if read fails
		if st, err := aggregate(clients); err == nil {
			currentSOC = st.SOC
		}

		var errs []string
		for _, c := range clients {
			var err error
			// evcc sends batterymode as integer: 1=normal, 2=hold, 3=charge
			// also accept strings for manual testing
			mode := strings.ToLower(req.Mode)
			switch mode {
			case "normal", "1":
				// "Auto" in the API = "Self consumption" in the Marstek app UI
				err = c.mc.SetAutoMode()
			case "hold", "2":
				// Hold: block discharge, allow solar charging
				// passive mode with power=0 lets solar charge but prevents grid discharge
				err = c.mc.SetPassiveMode(0, passiveCountdown)
			case "charge", "3":
				power := int(req.Power)
				if power <= 0 {
					power = defaultPowerPerDevice
				}
				// Marstek passive mode: negative = charge, positive = discharge
				err = c.mc.SetPassiveMode(-power, passiveCountdown)
			case "discharge", "4":
				// Discharge is not a standard evcc mode but supported for manual use
				if currentSOC <= minSOC {
					log.Printf("INFO: discharge blocked for %s — SOC %.0f%% ≤ min %.0f%%", c.name, currentSOC, minSOC)
					err = c.mc.SetAutoMode()
					break
				}
				power := int(req.Power)
				if power <= 0 {
					power = defaultPowerPerDevice
				}
				err = c.mc.SetPassiveMode(power, passiveCountdown)
			default:
				writeError(w, http.StatusBadRequest, "unknown mode: "+req.Mode)
				return
			}
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", c.name, err))
			}
		}

		if len(errs) > 0 {
			writeError(w, http.StatusBadGateway, strings.Join(errs, "; "))
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "mode": req.Mode})
	}
}

func main() {
	devCfg := devicesFromEnv()

	var clients []client
	for i, d := range devCfg {
		clients = append(clients, client{
			mc:   marstek.NewClient(d.Host, d.Port, udpTimeout),
			name: fmt.Sprintf("device%d(%s)", i+1, d.Host),
		})
	}

	log.Printf("marstek-api: proxying %d device(s)", len(clients))
	for _, c := range clients {
		log.Printf("  %s", c.name)
	}
	log.Printf("listening on %s", listenAddr)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/soc", handleSOC(clients))
	mux.HandleFunc("/api/power", handlePower(clients))
	mux.HandleFunc("/api/status", handleStatus(clients))
	mux.HandleFunc("/api/batterymode", handleBatteryMode(clients))
	mux.HandleFunc("/api/em", handleEM(clients))

	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}
