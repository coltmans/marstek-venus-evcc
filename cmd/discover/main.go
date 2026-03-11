// discover broadcasts Marstek.GetDevice and prints all responding devices.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	// Bind to the same port the devices use — they respond back to this port
	conn, err := net.ListenPacket("udp4", "0.0.0.0:30001")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	msg, _ := json.Marshal(map[string]any{
		"id":     0,
		"method": "Marstek.GetDevice",
		"params": map[string]string{"ble_mac": "0"},
	})

	port := 30000
	if p := os.Getenv("MARSTEK_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}

	targets := []*net.UDPAddr{
		{IP: net.IPv4bcast, Port: port},
	}

	// Also unicast to any IPs provided via MARSTEK_DEVICES
	if devices := os.Getenv("MARSTEK_DEVICES"); devices != "" {
		for _, h := range strings.Split(devices, ",") {
			h = strings.TrimSpace(h)
			if ip := net.ParseIP(h); ip != nil {
				targets = append(targets, &net.UDPAddr{IP: ip, Port: port})
			}
		}
	}

	for _, t := range targets {
		for i := 0; i < 3; i++ {
			conn.WriteTo(msg, t)
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Also try ES.GetStatus directly
	statusMsg, _ := json.Marshal(map[string]any{
		"id":     1,
		"method": "ES.GetStatus",
		"params": map[string]int{"id": 0},
	})
	for _, t := range targets[1:] {
		conn.WriteTo(statusMsg, t)
	}

	fmt.Printf("Sent to %d target(s). Listening 5 seconds...\n", len(targets))
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	seen := map[string]bool{}
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		if seen[addr.String()] {
			continue
		}
		seen[addr.String()] = true

		var pretty any
		if json.Unmarshal(buf[:n], &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Printf("\nFrom %s:\n%s\n", addr, b)
		} else {
			fmt.Printf("\nFrom %s (raw): %s\n", addr, buf[:n])
		}
	}

	if len(seen) == 0 {
		fmt.Println("No devices found. Check that the Open API is enabled in the Marstek app.")
	} else {
		fmt.Printf("\nFound %d device(s).\n", len(seen))
	}
}
