package domain

import (
	"errors"
	"fmt"
	"time"
)

// ============================================================================
// ERROS DO PROVIDER
// ============================================================================

// TransientError representa uma falha transitória que pode ser recuperada com retry
// Ex: timeout, erro de rede temporário
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("transient error: %v", e.Err)
}

func (e *TransientError) Unwrap() error {
	return e.Err
}

// IsTransientError verifica se um erro é TransientError usando errors.As
func IsTransientError(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}

// RateLimitError representa um erro 429 do provedor
// RetryAfter indica quanto tempo esperar antes da próxima tentativa
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded, retry after %v", e.RetryAfter)
}
