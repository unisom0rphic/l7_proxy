package mirroring

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type MirrorJob struct {
	method  string
	body    []byte
	header  http.Header
	targets []*url.URL
}

func NewJob(method string, body []byte, header http.Header, targets []*url.URL) *MirrorJob {
	return &MirrorJob{
		method:  method,
		body:    body,
		header:  header,
		targets: targets,
	}
}

type MirrorModule struct {
	client                *http.Client
	queue                 chan MirrorJob
	wg                    sync.WaitGroup
	sent, failed, dropped atomic.Uint64
}

func NewModule(workers, queueSize int) *MirrorModule {
	m := &MirrorModule{
		client: &http.Client{Timeout: 5 * time.Second},
		queue:  make(chan MirrorJob, queueSize),
	}

	for range workers {
		m.wg.Add(1)
		go m.worker()
	}
	return m
}

func (m *MirrorModule) Stop() { slog.Info("Closing mirroring module"); close(m.queue); m.wg.Wait() }

func (m *MirrorModule) TrySubmit(j MirrorJob) {
	select {
	case m.queue <- j:
	default:
		m.dropped.Add(1)
	}
}

func (m *MirrorModule) worker() {
	defer m.wg.Done()
	for j := range m.queue {
		// TODO: maybe sync.Pool for waitgroups?
		var wg sync.WaitGroup
		for _, t := range j.targets {
			wg.Add(1)
			go func(t *url.URL) {
				defer wg.Done()
				if err := m.send(t, j); err != nil {
					m.failed.Add(1)
					return
				}
				m.sent.Add(1)
			}(t)
		}
		wg.Wait()
	}
}

func (m *MirrorModule) send(t *url.URL, j MirrorJob) error {
	req, err := http.NewRequest(j.method, t.String(), bytes.NewReader(j.body))
	if err != nil {
		return err
	}
	req.Header = j.header.Clone()
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	// is it necessary to avoid leaks?
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}
