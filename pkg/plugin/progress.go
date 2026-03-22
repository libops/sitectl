package plugin

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type ProgressLine struct {
	out    *os.File
	frames []string
	index  int
	title  string
	detail string
	mu     sync.Mutex
	done   chan struct{}
	once   sync.Once
}

func NewProgressLine(w io.Writer, title, detail string) *ProgressLine {
	file, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return &ProgressLine{}
	}

	progress := &ProgressLine{
		out:    file,
		frames: []string{"-", "\\", "|", "/"},
		title:  strings.TrimSpace(title),
		detail: strings.TrimSpace(detail),
		done:   make(chan struct{}),
	}
	go progress.animate(120 * time.Millisecond)
	return progress
}

func (p *ProgressLine) Report(title, detail string) {
	if p == nil || p.out == nil {
		return
	}
	p.mu.Lock()
	p.title = strings.TrimSpace(title)
	p.detail = strings.TrimSpace(detail)
	p.renderLocked()
	p.mu.Unlock()
}

func (p *ProgressLine) Close() {
	if p == nil || p.out == nil {
		return
	}
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		defer p.mu.Unlock()
		_, _ = fmt.Fprint(p.out, "\r\033[2K")
	})
}

func (p *ProgressLine) animate(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			p.renderLocked()
			p.mu.Unlock()
		case <-p.done:
			return
		}
	}
}

func (p *ProgressLine) renderLocked() {
	if p.out == nil {
		return
	}
	frame := p.frames[p.index%len(p.frames)]
	p.index++
	line := strings.TrimSpace(strings.Join([]string{p.title, p.detail}, " - "))
	_, _ = fmt.Fprintf(p.out, "\r%s %s", frame, line)
}
