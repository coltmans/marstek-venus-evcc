package marstek

import (
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

type Request struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type Response struct {
	ID     int             `json:"id"`
	Src    string          `json:"src"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// ESStatus is the response from ES.GetStatus
type ESStatus struct {
	ID                   int      `json:"id"`
	BatSOC               *float64 `json:"bat_soc"`
	BatCap               *float64 `json:"bat_cap"`
	PVPower              *float64 `json:"pv_power"`
	OnGridPower          *float64 `json:"ongrid_power"`
	OffGridPower         *float64 `json:"offgrid_power"`
	BatPower             *float64 `json:"bat_power"`
	TotalPVEnergy        *float64 `json:"total_pv_energy"`
	TotalGridOutputEnergy *float64 `json:"total_grid_output_energy"`
	TotalGridInputEnergy  *float64 `json:"total_grid_input_energy"`
	TotalLoadEnergy       *float64 `json:"total_load_energy"`
}

// BatStatus is the response from Bat.GetStatus
type BatStatus struct {
	ID            int      `json:"id"`
	SOC           any      `json:"soc"` // can be number or string per the API
	ChargFlag     bool     `json:"charg_flag"`
	DisChargFlag  bool     `json:"dischrg_flag"`
	BatTemp       *float64 `json:"bat_temp"`
	BatCapacity   *float64 `json:"bat_capacity"`
	RatedCapacity *float64 `json:"rated_capacity"`
}

// EMStatus is the response from EM.GetStatus (energy meter / CT clamp)
type EMStatus struct {
	ID           int      `json:"id"`
	CTState      *int     `json:"ct_state"`
	APower       *float64 `json:"a_power"`
	BPower       *float64 `json:"b_power"`
	CPower       *float64 `json:"c_power"`
	TotalPower   *float64 `json:"total_power"`
	InputEnergy  *float64 `json:"input_energy"`
	OutputEnergy *float64 `json:"output_energy"`
}

type SetModeResult struct {
	ID        int  `json:"id"`
	SetResult bool `json:"set_result"`
}

// Client sends JSON-RPC commands to a Marstek device over UDP.
type Client struct {
	addr    string
	timeout time.Duration
	counter atomic.Int32
}

func NewClient(host string, port int, timeout time.Duration) *Client {
	return &Client{
		addr:    fmt.Sprintf("%s:%d", host, port),
		timeout: timeout,
	}
}

func (c *Client) call(method string, params any, result any) error {
	id := int(c.counter.Add(1))
	req := Request{ID: id, Method: method, Params: params}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	dst, err := net.ResolveUDPAddr("udp4", c.addr)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", c.addr, err)
	}

	// Bind to the same port as the device port — the device responds back to
	// the source port of the incoming packet, which must equal its own port.
	local, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", dst.Port))
	if err != nil {
		return fmt.Errorf("resolve local: %w", err)
	}

	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return fmt.Errorf("bind :%d: %w", dst.Port, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(c.timeout))

	if _, err := conn.WriteToUDP(data, dst); err != nil {
		return fmt.Errorf("write to %s: %w", c.addr, err)
	}

	buf := make([]byte, 8192)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return fmt.Errorf("read from %s: %w", c.addr, err)
	}

	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return resp.Error
	}

	if result != nil && resp.Result != nil {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

// GetESStatus queries the Energy System status (SOC, power flows, energy totals).
func (c *Client) GetESStatus() (*ESStatus, error) {
	var result ESStatus
	err := c.call("ES.GetStatus", map[string]int{"id": 0}, &result)
	return &result, err
}

// GetEMStatus queries the energy meter / CT clamp data.
func (c *Client) GetEMStatus() (*EMStatus, error) {
	var result EMStatus
	err := c.call("EM.GetStatus", map[string]int{"id": 0}, &result)
	return &result, err
}

// GetBatStatus queries battery info (SOC, temperature, capacity, charge flags).
func (c *Client) GetBatStatus() (*BatStatus, error) {
	var result BatStatus
	err := c.call("Bat.GetStatus", map[string]int{"id": 0}, &result)
	return &result, err
}

// SetAutoMode restores the battery to automatic control.
func (c *Client) SetAutoMode() error {
	params := map[string]any{
		"id": 0,
		"config": map[string]any{
			"mode":     "Auto",
			"auto_cfg": map[string]any{"enable": 1},
		},
	}
	var r SetModeResult
	if err := c.call("ES.SetMode", params, &r); err != nil {
		return err
	}
	if !r.SetResult {
		return fmt.Errorf("SetAutoMode: device returned false")
	}
	return nil
}

// SetPassiveMode sets a direct power setpoint.
// powerW > 0 = charge, powerW < 0 = discharge (sign convention TBD — verify against your device).
// countdownSec: the battery reverts to its previous mode after this many seconds.
func (c *Client) SetPassiveMode(powerW int, countdownSec int) error {
	if countdownSec <= 0 {
		countdownSec = 90 // safe default: revert after 90 s if evcc stops sending
	}
	params := map[string]any{
		"id": 0,
		"config": map[string]any{
			"mode": "Passive",
			"passive_cfg": map[string]any{
				"power":   powerW,
				"cd_time": countdownSec,
			},
		},
	}
	var r SetModeResult
	if err := c.call("ES.SetMode", params, &r); err != nil {
		return err
	}
	if !r.SetResult {
		return fmt.Errorf("SetPassiveMode: device returned false")
	}
	return nil
}
