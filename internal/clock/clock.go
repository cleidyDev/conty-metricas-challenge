package clock

import (
	"context"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, durantion time.Duration) error
}

type SystemClock struct{}

func NewSystemClock() *SystemClock {
	return &SystemClock{}
}

func (c *SystemClock) Now() time.Time {
	return time.Now()
}

func (c *SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FAKE CLOCK (TESTES)

type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{
		now: start,
	}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Registrar a chamada para asserts
	c.sleeps = append(c.sleeps, d)

	// Avançar o relógio
	c.now = c.now.Add(d)

	return nil
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *FakeClock) GetSleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration{}, c.sleeps...)
}

func (c *FakeClock) ResetSleeps() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = nil
}
