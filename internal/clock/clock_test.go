package clock

import (
	"context"
	"sync"
	"testing"
	"time"
)

var refTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

// ============================================================================
// SYSTEM CLOCK
// ============================================================================

func TestSystemClock_Now_Avanca(t *testing.T) {
	c := NewSystemClock()
	antes := c.Now()
	time.Sleep(2 * time.Millisecond)
	depois := c.Now()

	if !depois.After(antes) {
		t.Fatalf("expected Now() to advance, got %v then %v", antes, depois)
	}
}

func TestSystemClock_Sleep_DuraAproximadamenteODefinido(t *testing.T) {
	c := NewSystemClock()
	inicio := time.Now()

	if err := c.Sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("unexpected sleep error: %v", err)
	}

	decorrido := time.Since(inicio)
	if decorrido < 20*time.Millisecond {
		t.Fatalf("expected to sleep at least 20ms, slept %v", decorrido)
	}
	if decorrido > time.Second {
		t.Fatalf("expected to sleep about 20ms, slept %v", decorrido)
	}
}

func TestSystemClock_Sleep_ZeroRetornaImediatamente(t *testing.T) {
	c := NewSystemClock()
	inicio := time.Now()

	if err := c.Sleep(context.Background(), 0); err != nil {
		t.Fatalf("unexpected sleep error: %v", err)
	}

	if decorrido := time.Since(inicio); decorrido > time.Second {
		t.Fatalf("expected immediate return, took %v", decorrido)
	}
}

func TestSystemClock_Sleep_ContextoCancelado(t *testing.T) {
	c := NewSystemClock()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	inicio := time.Now()
	err := c.Sleep(ctx, 10*time.Second)
	decorrido := time.Since(inicio)

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Não pode esperar o sleep completo: tem de notar o cancelamento.
	if decorrido > time.Second {
		t.Fatalf("expected sleep to abort on cancel, took %v", decorrido)
	}
}

func TestSystemClock_Sleep_ContextoJaCancelado(t *testing.T) {
	c := NewSystemClock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Sleep(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSystemClock_SatisfiesInterface(t *testing.T) {
	// Compila apenas se SystemClock implementa Clock.
	var c Clock = NewSystemClock()
	_ = c.Now()
}

// ============================================================================
// FAKE CLOCK
// ============================================================================

func TestFakeClock_IniciaNoTempoDado(t *testing.T) {
	c := NewFakeClock(refTime)
	if got := c.Now(); !got.Equal(refTime) {
		t.Fatalf("expected %v, got %v", refTime, got)
	}
}

func TestFakeClock_Sleep_AvancaORelogioSemEsperaReal(t *testing.T) {
	c := NewFakeClock(refTime)

	inicio := time.Now()
	if err := c.Sleep(context.Background(), 7*time.Second); err != nil {
		t.Fatalf("unexpected sleep error: %v", err)
	}
	real := time.Since(inicio)

	if real > time.Second {
		t.Fatalf("FakeClock must not wait in real time, took %v", real)
	}
	if got, want := c.Now(), refTime.Add(7*time.Second); !got.Equal(want) {
		t.Fatalf("expected clock at %v, got %v", want, got)
	}
}

func TestFakeClock_Sleep_RegistraCadaDuracao(t *testing.T) {
	cases := []struct {
		name      string
		durations []time.Duration
	}{
		{"nenhuma espera", nil},
		{"uma espera", []time.Duration{7 * time.Second}},
		{"backoff exponencial", []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}},
		{"429 repetido", []time.Duration{time.Second, time.Second, time.Second}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewFakeClock(refTime)
			for _, d := range tc.durations {
				if err := c.Sleep(context.Background(), d); err != nil {
					t.Fatalf("unexpected sleep error: %v", err)
				}
			}

			sleeps := c.GetSleeps()
			if len(sleeps) != len(tc.durations) {
				t.Fatalf("expected %d sleeps (%v), got %d (%v)",
					len(tc.durations), tc.durations, len(sleeps), sleeps)
			}
			for i, want := range tc.durations {
				if sleeps[i] != want {
					t.Fatalf("sleep[%d]: expected %v, got %v", i, want, sleeps[i])
				}
			}
			// O relógio soma tudo o que foi esperdo.
			want := refTime
			for _, d := range tc.durations {
				want = want.Add(d)
			}
			if got := c.Now(); !got.Equal(want) {
				t.Fatalf("expected clock at %v, got %v", want, got)
			}
		})
	}
}

func TestFakeClock_Sleep_ContextoCanceladoNaoAvanca(t *testing.T) {
	c := NewFakeClock(refTime)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Sleep(ctx, 5*time.Second); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !c.Now().Equal(refTime) {
		t.Fatalf("expected clock unchanged, got %v", c.Now())
	}
	if len(c.GetSleeps()) != 0 {
		t.Fatalf("expected no recorded sleeps, got %v", c.GetSleeps())
	}
}

func TestFakeClock_Advance(t *testing.T) {
	c := NewFakeClock(refTime)

	c.Advance(time.Hour)
	if got, want := c.Now(), refTime.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}

	c.Advance(30 * time.Minute)
	if got, want := c.Now(), refTime.Add(90*time.Minute); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFakeClock_Advance_AntesESleep(t *testing.T) {
	c := NewFakeClock(refTime)
	c.Advance(time.Hour)
	if err := c.Sleep(context.Background(), time.Minute); err != nil {
		t.Fatalf("unexpected sleep error: %v", err)
	}
	if got, want := c.Now(), refTime.Add(61*time.Minute); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFakeClock_ResetSleeps(t *testing.T) {
	c := NewFakeClock(refTime)
	_ = c.Sleep(context.Background(), time.Second)
	_ = c.Sleep(context.Background(), 2*time.Second)

	c.ResetSleeps()

	if sleeps := c.GetSleeps(); len(sleeps) != 0 {
		t.Fatalf("expected no sleeps after reset, got %v", sleeps)
	}
	// O tempo já avançado não é desfeito: só o histórico é limpo.
	if got, want := c.Now(), refTime.Add(3*time.Second); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFakeClock_GetSleeps_RetornaCopia(t *testing.T) {
	c := NewFakeClock(refTime)
	_ = c.Sleep(context.Background(), time.Second)

	sleeps := c.GetSleeps()
	sleeps[0] = 99 * time.Hour // mexer no retorno não pode afetar o relógio

	novo := c.GetSleeps()
	if len(novo) != 1 || novo[0] != time.Second {
		t.Fatalf("expected the internal sleeps to be untouched, got %v", novo)
	}
}

func TestFakeClock_ResetSleeps_DepoisDeSleep(t *testing.T) {
	c := NewFakeClock(refTime)
	_ = c.Sleep(context.Background(), time.Second)
	c.ResetSleeps()
	_ = c.Sleep(context.Background(), 2*time.Second)

	sleeps := c.GetSleeps()
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("expected only the sleep after the reset, got %v", sleeps)
	}
}

// ============================================================================
// CONCORRÊNCIA (corre com -race)
// ============================================================================

func TestFakeClock_Concorrente(t *testing.T) {
	c := NewFakeClock(refTime)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Sleep(context.Background(), time.Millisecond)
			c.Advance(time.Millisecond)
			_ = c.Now()
			_ = c.GetSleeps()
		}()
	}
	wg.Wait()

	// 50 sleeps + 50 advances de 1ms.
	if got, want := c.Now(), refTime.Add(100*time.Millisecond); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	if got := len(c.GetSleeps()); got != 50 {
		t.Fatalf("expected 50 recorded sleeps, got %d", got)
	}
}

func TestFakeClock_SatisfiesInterface(t *testing.T) {
	// Compila apenas se FakeClock implementa Clock.
	var c Clock = NewFakeClock(refTime)
	_ = c.Now()
}
