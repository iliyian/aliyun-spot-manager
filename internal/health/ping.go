package health

import (
	"fmt"
	"time"

	"github.com/go-ping/ping"
)

// PingChecker performs ICMP ping health checks
type PingChecker struct {
	timeout time.Duration
	count   int
}

// NewPingChecker creates a new ping checker
func NewPingChecker() *PingChecker {
	return &PingChecker{
		timeout: 5 * time.Second,
		count:   3,
	}
}

// Check performs a ping check on the given IP address
func (p *PingChecker) Check(ip string) error {
	if ip == "" {
		return fmt.Errorf("no IP address provided")
	}

	pinger, err := ping.NewPinger(ip)
	if err != nil {
		return fmt.Errorf("failed to create pinger: %w", err)
	}

	pinger.Count = p.count
	pinger.Timeout = p.timeout
	// On Linux, we need to run as root or set capabilities for ICMP
	// Using UDP mode as fallback which doesn't require root
	pinger.SetPrivileged(false)

	err = pinger.Run()
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return fmt.Errorf("no ping response from %s", ip)
	}

	return nil
}

// WaitForHealth waits for the instance to become healthy
func (p *PingChecker) WaitForHealth(ip string, timeout, interval time.Duration) error {
	if ip == "" {
		return fmt.Errorf("no IP address provided")
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := p.Check(ip); err == nil {
			return nil
		}
		time.Sleep(interval)
	}

	return fmt.Errorf("health check timeout after %v", timeout)
}