package porttest

import (
	"context"
	"sync"

	"myapp.v1.0.0/internal/example/application/dto"
)

// RecordingExampleEventPublisher registra los eventos publicados. Err, si no es nil,
// hace fallar la publicación (fallo técnico del outbox).
type RecordingExampleEventPublisher struct {
	mu     sync.Mutex
	Events []dto.ExampleStatusChanged
	Err    error
}

// StatusChanged registra el evento.
func (p *RecordingExampleEventPublisher) StatusChanged(_ context.Context, e dto.ExampleStatusChanged) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Err != nil {
		return p.Err
	}
	p.Events = append(p.Events, e)
	return nil
}
