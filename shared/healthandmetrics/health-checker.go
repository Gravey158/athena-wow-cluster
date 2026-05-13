package healthandmetrics

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type HealthCheckObject interface {
	HealthCheckAddress() string
}

type HealthCheckSuccessObserver func(HealthCheckObject)

type HealthCheckFailedObserver func(HealthCheckObject, error)

type HealthChecker interface {
	AddHealthCheckObject(HealthCheckObject) error
	RemoveHealthCheckObject(HealthCheckObject) error
	AddSuccessObserver(HealthCheckSuccessObserver)
	AddFailedObserver(HealthCheckFailedObserver)
	// Start runs the periodic health-check loop until ctx is canceled (B4).
	// Previously Start() had no ctx and its goroutines leaked on shutdown.
	Start(ctx context.Context) error
}

type HealthCheckProcessor interface {
	Check(HealthCheckObject) error
}

type healthCheckResult struct {
	obj HealthCheckObject
	err error
}

type healthChecker struct {
	delay           time.Duration
	processorsCount int

	processor HealthCheckProcessor

	objectsMu sync.RWMutex
	objects   []HealthCheckObject

	observersMu      sync.RWMutex
	successObservers []HealthCheckSuccessObserver
	failedObservers  []HealthCheckFailedObserver

	results chan healthCheckResult
	queue   chan HealthCheckObject
}

func NewHealthChecker(delay time.Duration, processorsCount int, processor HealthCheckProcessor) HealthChecker {
	return &healthChecker{
		delay:           delay,
		processorsCount: processorsCount,
		processor:       processor,
		results:         make(chan healthCheckResult, 100),
		queue:           make(chan HealthCheckObject, 100),
	}
}

func (h *healthChecker) AddHealthCheckObject(object HealthCheckObject) error {
	h.objectsMu.Lock()
	defer h.objectsMu.Unlock()

	// Check if we already have this observable.
	for _, o := range h.objects {
		if o.HealthCheckAddress() == object.HealthCheckAddress() {
			return nil
		}
	}

	h.objects = append(h.objects, object)
	return nil
}

func (h *healthChecker) RemoveHealthCheckObject(object HealthCheckObject) error {
	h.objectsMu.Lock()
	defer h.objectsMu.Unlock()

	for i := range h.objects {
		if h.objects[i].HealthCheckAddress() == object.HealthCheckAddress() {
			h.objects = append(h.objects[:i], h.objects[i+1:]...)
			return nil
		}
	}

	return nil
}

func (h *healthChecker) AddSuccessObserver(observer HealthCheckSuccessObserver) {
	h.observersMu.Lock()
	defer h.observersMu.Unlock()

	h.successObservers = append(h.successObservers, observer)
}

func (h *healthChecker) AddFailedObserver(observer HealthCheckFailedObserver) {
	h.observersMu.Lock()
	defer h.observersMu.Unlock()

	h.failedObservers = append(h.failedObservers, observer)
}

func (h *healthChecker) Start(ctx context.Context) error {
	go h.process(ctx, h.processorsCount)

	for {
		start := time.Now()
		if err := h.makeIteration(ctx); err != nil {
			// ctx canceled mid-iteration; close the worker-queue so workers
			// drain + exit, then return cleanly.
			close(h.queue)
			return nil
		}
		waitTime := h.delay - time.Since(start)
		if waitTime > 0 {
			select {
			case <-time.After(waitTime):
			case <-ctx.Done():
				close(h.queue)
				return nil
			}
		} else if ctx.Err() != nil {
			close(h.queue)
			return nil
		}
	}
}

func (h *healthChecker) makeIteration(ctx context.Context) error {
	// Copy the objects snapshot under the lock; release the lock before
	// blocking on channel sends so AddHealthCheckObject can proceed in
	// parallel.
	h.objectsMu.Lock()
	objects := make([]HealthCheckObject, len(h.objects))
	copy(objects, h.objects)
	h.objectsMu.Unlock()

	if len(objects) == 0 {
		return nil
	}

	for i := range objects {
		select {
		case h.queue <- objects[i]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for i := 0; i < len(objects); i++ {
		select {
		case result := <-h.results:
			h.handleResult(result)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (h *healthChecker) handleResult(result healthCheckResult) {
	if result.err != nil {
		h.RemoveHealthCheckObject(result.obj)

		h.observersMu.RLock()
		defer h.observersMu.RUnlock()

		for _, observer := range h.failedObservers {
			observer(result.obj, result.err)
		}
	} else {
		h.observersMu.RLock()
		defer h.observersMu.RUnlock()

		for _, observer := range h.successObservers {
			observer(result.obj)
		}
	}
}

func (h *healthChecker) process(ctx context.Context, processorsCount int) {
	for i := 0; i < processorsCount; i++ {
		go func() {
			for obj := range h.queue {
				// select-wrap the result-send so worker exits on ctx cancel
				// even if nobody is reading the result channel.
				select {
				case h.results <- healthCheckResult{
					obj: obj,
					err: h.processor.Check(obj),
				}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
}

type httpHealthCheckProcessor struct {
	client *http.Client
}

func NewHttpHealthCheckProcessor(timeout time.Duration) HealthCheckProcessor {
	return &httpHealthCheckProcessor{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (h *httpHealthCheckProcessor) Check(object HealthCheckObject) error {
	resp, err := h.client.Get("http://" + object.HealthCheckAddress() + HealthCheckURL)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status code %d", resp.StatusCode)
	}

	return nil
}
