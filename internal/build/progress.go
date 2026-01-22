package build

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Phase represents a build phase
type Phase string

const (
	PhaseTriggering   Phase = "trigger"
	PhaseWaitingStart Phase = "waiting"
	PhaseBuilding     Phase = "building"
	PhaseDownloading  Phase = "download"
)

// PhaseInfo contains information about a phase
type PhaseInfo struct {
	Name   string
	Icon   string
	Status string
}

var phaseInfos = map[Phase]PhaseInfo{
	PhaseTriggering:   {Name: "Trigger", Icon: "🚀"},
	PhaseWaitingStart: {Name: "Start", Icon: "⏳"},
	PhaseBuilding:     {Name: "Build", Icon: "🔨"},
	PhaseDownloading:  {Name: "Download", Icon: "⬇️"},
}

// Progress tracks and displays build progress
type Progress struct {
	writer           io.Writer
	buildID          string
	startTime        time.Time
	currentPhase     Phase
	workflowURL      string
	lastDownloadPct  int
	lastDownloadTime time.Time
	mu               sync.Mutex
}

// NewProgress creates a new progress reporter
func NewProgress(w io.Writer) *Progress {
	return &Progress{
		writer: w,
	}
}

// Start begins progress tracking for a build
func (p *Progress) Start(buildID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buildID = buildID
	p.startTime = time.Now()

	fmt.Fprintf(p.writer, "\n")
	fmt.Fprintf(p.writer, "🏗️  Builder - Remote iOS Build\n")
	fmt.Fprintf(p.writer, "   Build ID: %s\n", buildID)
	fmt.Fprintf(p.writer, "\n")
}

// Update updates the current phase with a message
func (p *Progress) Update(phase Phase, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.currentPhase = phase
	info := phaseInfos[phase]

	// Clear line with ANSI escape and print update
	fmt.Fprintf(p.writer, "\r\033[K%s  %s: %s", info.Icon, info.Name, message)
}

// Complete marks a phase as complete
func (p *Progress) Complete(phase Phase, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := phaseInfos[phase]
	elapsed := time.Since(p.startTime).Round(time.Second)

	// Clear line and print completion
	fmt.Fprintf(p.writer, "\r\033[K✅ %s: %s (%s)\n", info.Name, message, elapsed)
}

// Error marks a phase as failed
func (p *Progress) Error(phase Phase, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := phaseInfos[phase]
	elapsed := time.Since(p.startTime).Round(time.Second)

	fmt.Fprintf(p.writer, "\r\033[K❌ %s: Failed (%s)\n", info.Name, elapsed)
	fmt.Fprintf(p.writer, "   Error: %v\n", err)

	if p.workflowURL != "" {
		fmt.Fprintf(p.writer, "   Logs: %s\n", p.workflowURL)
	}
}

// SetWorkflowURL sets the workflow URL for error messages
func (p *Progress) SetWorkflowURL(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workflowURL = url
}

// UpdateDownloadProgress updates the download progress display
func (p *Progress) UpdateDownloadProgress(downloaded, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	percent := int(float64(downloaded) / float64(total) * 100)
	now := time.Now()

	// Throttle: only update on percentage change or every 500ms
	if percent == p.lastDownloadPct && now.Sub(p.lastDownloadTime) < 500*time.Millisecond {
		return
	}
	p.lastDownloadPct = percent
	p.lastDownloadTime = now

	info := phaseInfos[PhaseDownloading]
	downloadedMB := float64(downloaded) / (1024 * 1024)
	totalMB := float64(total) / (1024 * 1024)

	// Create progress bar
	barWidth := 20
	filled := percent * barWidth / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	fmt.Fprintf(p.writer, "\r\033[K%s  %s: [%s] %d%% (%.1f/%.1f MB)",
		info.Icon, info.Name, bar, percent, downloadedMB, totalMB)
}

// Finish completes progress tracking
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()

	elapsed := time.Since(p.startTime).Round(time.Second)

	fmt.Fprintf(p.writer, "\n")
	fmt.Fprintf(p.writer, "✨ Build complete! Total time: %s\n", elapsed)
	fmt.Fprintf(p.writer, "\n")
}
